package app

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// ---- a tiny in-memory WebDAV server ----

type davFake struct {
	mu    sync.Mutex
	user  string
	pass  string
	dirs  map[string]bool
	files map[string][]byte
	calls []string // "METHOD path"
}

func newDAVFake(user, pass string) *davFake {
	return &davFake{user: user, pass: pass, dirs: map[string]bool{"/dav": true}, files: map[string][]byte{}}
}

func (d *davFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p := strings.TrimSuffix(r.URL.Path, "/")
	d.calls = append(d.calls, r.Method+" "+p)
	if u, pw, ok := r.BasicAuth(); !ok || u != d.user || pw != d.pass {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case "MKCOL":
		if d.dirs[p] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !d.dirs[path.Dir(p)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		d.dirs[p] = true
		w.WriteHeader(http.StatusCreated)
	case http.MethodPut:
		if !d.dirs[path.Dir(p)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		data, _ := io.ReadAll(r.Body)
		d.files[p] = data
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		data, ok := d.files[p]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	case http.MethodDelete:
		if _, ok := d.files[p]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(d.files, p)
		w.WriteHeader(http.StatusNoContent)
	case "PROPFIND":
		_, isFile := d.files[p]
		if !d.dirs[p] && !isFile {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">`)
		entry := func(href string, dir bool, size int) {
			fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop>`, href)
			if dir {
				b.WriteString(`<D:resourcetype><D:collection/></D:resourcetype>`)
			} else {
				fmt.Fprintf(&b, `<D:resourcetype/><D:getcontentlength>%d</D:getcontentlength><D:getlastmodified>Sun, 08 Nov 2026 13:30:00 GMT</D:getlastmodified>`, size)
			}
			b.WriteString(`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		}
		if isFile {
			entry(url.PathEscape(p), false, len(d.files[p]))
		} else {
			entry(p+"/", true, 0)
			if r.Header.Get("Depth") != "0" {
				var names []string
				for f := range d.files {
					if path.Dir(f) == p {
						names = append(names, f)
					}
				}
				sort.Strings(names)
				for _, f := range names {
					entry((&url.URL{Path: f}).EscapedPath(), false, len(d.files[f]))
				}
				for dir := range d.dirs {
					if path.Dir(dir) == p && dir != p {
						entry(dir+"/", true, 0)
					}
				}
			}
		}
		b.WriteString(`</D:multistatus>`)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, b.String())
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (d *davFake) names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for f := range d.files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// webdavHarness is a service pointed at a fake WebDAV server, with the
// password in the file fallback (Secrets is nil in tests).
func webdavHarness(t *testing.T) (*harness, *davFake) {
	t.Helper()
	dav := newDAVFake("alice", "s3cret")
	srv := httptest.NewServer(dav)
	t.Cleanup(srv.Close)
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.WebDAV = model.WebDAVSettings{URL: srv.URL + "/dav", User: "alice", Dir: "lanflow", Keep: 2}
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetWebDAVPassword("s3cret"); err != nil {
		t.Fatal(err)
	}
	return h, dav
}

func TestWebDAVPasswordFileFallback(t *testing.T) {
	h, _ := webdavHarness(t)
	p := filepath.Join(h.dir, webdavSecretFile)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("secret file: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "s3cret") {
		t.Fatalf("password stored in clear: %q", raw)
	}
	info := h.s.WebDAVInfo()
	if !info.HasPassword || info.PasswordStore != model.PasswordStoreFile {
		t.Fatalf("info = %+v", info)
	}
	// Neither the settings file nor an export carries it.
	for _, f := range []string{filepath.Join(h.dir, settingsFile)} {
		data, _ := os.ReadFile(f)
		if strings.Contains(string(data), "s3cret") {
			t.Fatalf("%s holds the password", f)
		}
	}
	// A fresh service reads it back from the file.
	s2, err := newServiceWith(Options{DataDir: h.dir, LogDir: h.dir, CacheDir: h.dir, ResourceDir: h.dir}, h.s.d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s2.Shutdown)
	if pw, err := s2.loadPassword(); err != nil || pw != "s3cret" {
		t.Fatalf("reloaded password = %q, %v", pw, err)
	}
	// Removing it deletes the file.
	if err := h.s.SetWebDAVPassword(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("secret file still there: %v", err)
	}
	if h.s.WebDAVInfo().HasPassword {
		t.Fatal("HasPassword after removal")
	}
}

func TestWebDAVBackupListRestoreDelete(t *testing.T) {
	h, dav := webdavHarness(t)
	if err := h.s.TestWebDAV(); err != nil {
		t.Fatalf("TestWebDAV: %v", err)
	}
	dav.mu.Lock()
	if !dav.dirs["/dav/lanflow"] {
		dav.mu.Unlock()
		t.Fatal("remote directory not created")
	}
	dav.mu.Unlock()

	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)
	h.s.d.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if err := h.s.BackupNow(); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
		now = now.Add(time.Hour)
	}
	// Keep: 2 → the oldest was pruned.
	if got := dav.names(); len(got) != 2 || got[0] != "/dav/lanflow/lanflow-backup-20261110-100000.yaml" || got[1] != "/dav/lanflow/lanflow-backup-20261110-110000.yaml" {
		t.Fatalf("remote files = %q", got)
	}
	if last := h.s.Settings().WebDAV.LastBackupAt; !last.Equal(time.Date(2026, 11, 10, 11, 0, 0, 0, time.Local)) {
		t.Fatalf("LastBackupAt = %v", last)
	}
	// The upload is the export with the subscriptions.
	ex, err := syncfile.Decode(dav.files["/dav/lanflow/lanflow-backup-20261110-110000.yaml"], defaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Subscriptions) != 1 || ex.Subscriptions[0].URL != "https://example.com/sub" || ex.Settings.MixedPort != 7890 {
		t.Fatalf("backup document = %+v", ex)
	}
	if strings.Contains(string(dav.files["/dav/lanflow/lanflow-backup-20261110-110000.yaml"]), "s3cret") {
		t.Fatal("backup carries the password")
	}

	list, err := h.s.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "lanflow-backup-20261110-110000.yaml" || list[1].Name != "lanflow-backup-20261110-100000.yaml" || list[0].Size == 0 {
		t.Fatalf("list = %+v", list)
	}
	if !list[0].Modified.Equal(time.Date(2026, 11, 10, 11, 0, 0, 0, time.Local)) {
		t.Fatalf("Modified = %v", list[0].Modified)
	}

	// Restore: change a setting, restore the backup, the old value is back.
	set := h.s.Settings()
	set.MixedPort = 7899
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	summary, err := h.s.RestoreBackup(list[0].Name)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if h.s.Settings().MixedPort != 7890 || !strings.Contains(summary, "设置") {
		t.Fatalf("after restore: port %d, summary %q", h.s.Settings().MixedPort, summary)
	}
	if _, err := h.s.RestoreBackup("evil.json"); err == nil {
		t.Fatal("restore accepted a foreign name")
	}

	if err := h.s.DeleteBackup(list[1].Name); err != nil {
		t.Fatal(err)
	}
	if got := dav.names(); len(got) != 1 {
		t.Fatalf("after delete: %q", got)
	}
	list, _ = h.s.ListBackups()
	if len(list) != 1 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestWebDAVErrors(t *testing.T) {
	h, _ := webdavHarness(t)
	// Wrong password → a login error, nothing uploaded.
	if err := h.s.SetWebDAVPassword("wrong"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.BackupNow(); err == nil || !strings.Contains(err.Error(), "登录被拒绝") {
		t.Fatalf("err = %v", err)
	}
	// No server address.
	set := h.s.Settings()
	set.WebDAV.URL = ""
	_ = h.s.SaveSettings(set)
	if _, err := h.s.ListBackups(); err == nil {
		t.Fatal("listing without an address succeeded")
	}
	// Nothing backed up yet is an empty list, not an error.
	h2, _ := webdavHarness(t)
	if list, err := h2.s.ListBackups(); err != nil || len(list) != 0 {
		t.Fatalf("empty list = %+v, %v", list, err)
	}
}

func TestAutoBackupDue(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	set := model.WebDAVSettings{URL: "https://dav.example.com", AutoBackup: model.WebDAVDaily}
	if !autoBackupDue(set, time.Time{}, now) {
		t.Fatal("never backed up: due")
	}
	set.LastBackupAt = now.Add(-2 * time.Hour)
	if autoBackupDue(set, time.Time{}, now) {
		t.Fatal("backed up 2 h ago: not due")
	}
	set.LastBackupAt = now.Add(-25 * time.Hour)
	if !autoBackupDue(set, time.Time{}, now) {
		t.Fatal("backed up 25 h ago: due")
	}
	if autoBackupDue(set, now.Add(-10*time.Minute), now) {
		t.Fatal("a failed attempt 10 min ago: wait")
	}
	set.AutoBackup = model.WebDAVWeekly
	if autoBackupDue(set, time.Time{}, now) {
		t.Fatal("weekly, 25 h ago: not due")
	}
	set.AutoBackup = ""
	set.LastBackupAt = time.Time{}
	if autoBackupDue(set, time.Time{}, now) {
		t.Fatal("off: never due")
	}
}

func TestAutoBackupTick(t *testing.T) {
	h, dav := webdavHarness(t)
	set := h.s.Settings()
	set.WebDAV.AutoBackup = model.WebDAVDaily
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.webdavTick()
	deadline := time.Now().Add(5 * time.Second)
	for len(dav.names()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(dav.names()); n != 1 {
		t.Fatalf("automatic backup: %d files", n)
	}
	// Due again only after a day: a second tick uploads nothing.
	h.s.webdavTick()
	time.Sleep(50 * time.Millisecond)
	if n := len(dav.names()); n != 1 {
		t.Fatalf("second tick uploaded: %d files", n)
	}
}

func TestParseMultistatus(t *testing.T) {
	data := `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>http://x/dav/lanflow/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>` +
		`<d:response><d:href>/dav/lanflow/lanflow-backup-20261108-213000.json</d:href><d:propstat><d:prop><d:getcontentlength>42</d:getcontentlength><d:getlastmodified>Sun, 08 Nov 2026 13:30:00 GMT</d:getlastmodified><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`
	got, err := parseMultistatus([]byte(data), "http://x/dav/lanflow")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "lanflow-backup-20261108-213000.json" || got[0].Size != 42 || got[0].Dir || got[0].Modified.IsZero() {
		t.Fatalf("entries = %+v", got)
	}
	if _, err := parseMultistatus([]byte("<html>"), "x"); err == nil {
		t.Fatal("garbage parsed")
	}
	var ms multistatus
	if err := xml.Unmarshal([]byte(data), &ms); err != nil || len(ms.Responses) != 2 {
		t.Fatalf("raw unmarshal: %v %d", err, len(ms.Responses))
	}
}

func TestObfuscateRoundTrip(t *testing.T) {
	for _, pw := range []string{"", "a", "p@ss wörd 密码", strings.Repeat("x", 200)} {
		if got := deobfuscate(obfuscate(pw)); got != pw {
			t.Fatalf("round trip %q = %q", pw, got)
		}
	}
	if deobfuscate("not base64!") != "" || deobfuscate("YWJj") != "" {
		t.Fatal("garbage must give an empty password")
	}
}

// ---- Wi-Fi rules ----

func TestWiFiRulesSwitchSubscription(t *testing.T) {
	h := newHarness(t, nil)
	office, _ := h.store.Put(model.Subscription{Name: "办公", Source: "url", URL: "https://example.com/office"})
	_ = h.store.SetContent(office.ID, []byte("proxies: []\n"))
	home := h.s.Status().CurrentSubscription
	set := h.s.Settings()
	set.SSIDRules = []model.SSIDRule{{SSID: "Office-WiFi", SubscriptionID: office.ID}}
	set.SSIDDefaultSub = home
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	ssid := ""
	h.s.d.netinfo = func(context.Context) (model.NetInfo, error) {
		return model.NetInfo{LANIP: "192.168.1.23", SSID: ssid}, nil
	}

	// No SSID (wired, or macOS hiding it): nothing happens.
	h.s.recheck()
	if cur := h.s.Status().CurrentSubscription; cur != home {
		t.Fatalf("switched without an SSID: %s", cur)
	}
	// Joining the office network switches (case-insensitively), with a toast.
	ssid = "office-wifi"
	h.s.recheck()
	if cur := h.s.Status().CurrentSubscription; cur != office.ID {
		t.Fatalf("not switched: %s", cur)
	}
	var toast bool
	for _, n := range h.s.TakeNotices() {
		if n.Kind == "toast" && strings.Contains(n.Title, "办公") && strings.Contains(n.Title, "office-wifi") {
			toast = true
		}
	}
	if !toast {
		t.Fatal("no toast about the switch")
	}
	// The same network again: the user's manual choice stays.
	if err := h.s.UseSubscription(home); err != nil {
		t.Fatal(err)
	}
	h.s.wifiCheck()
	if cur := h.s.Status().CurrentSubscription; cur != home {
		t.Fatalf("re-applied on the same network: %s", cur)
	}
	// Another network without a rule: the default subscription.
	_ = h.s.UseSubscription(office.ID)
	ssid = "Cafe"
	h.s.recheck()
	if cur := h.s.Status().CurrentSubscription; cur != home {
		t.Fatalf("default not applied: %s", cur)
	}
	// A rule pointing at a deleted subscription is skipped.
	set = h.s.Settings()
	set.SSIDRules = []model.SSIDRule{{SSID: "Gone", SubscriptionID: "s999"}}
	set.SSIDDefaultSub = ""
	_ = h.s.SaveSettings(set)
	ssid = "Gone"
	h.s.recheck()
	if cur := h.s.Status().CurrentSubscription; cur != home {
		t.Fatalf("switched to a missing subscription: %s", cur)
	}
}

func TestWiFiTarget(t *testing.T) {
	set := model.Settings{SSIDRules: []model.SSIDRule{{SSID: " Home ", SubscriptionID: "a"}, {SSID: "Work", SubscriptionID: ""}}, SSIDDefaultSub: "d"}
	for ssid, want := range map[string]string{"home": "a", "Home": "a", "Work": "d", "Other": "d", "": "", "  ": ""} {
		if got := wifiTarget(set, ssid); got != want {
			t.Errorf("wifiTarget(%q) = %q, want %q", ssid, got, want)
		}
	}
}

// ---- download mirror ----

func TestMirrorURL(t *testing.T) {
	const raw = "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.33/mihomo-darwin-arm64-v1.19.33.gz"
	cases := []struct{ mirror, raw, want string }{
		{"", raw, raw},
		{"https://ghfast.top/", raw, "https://ghfast.top/" + raw},
		{"https://ghfast.top", raw, "https://ghfast.top/" + raw},
		{"https://ghfast.top/", "https://ghfast.top/" + raw, "https://ghfast.top/" + raw},
		{"https://ghfast.top/", "https://objects.githubusercontent.com/x/y", "https://ghfast.top/https://objects.githubusercontent.com/x/y"},
		{"https://ghfast.top/", "https://example.com/file", "https://example.com/file"},
		{"https://ghfast.top/", "", ""},
	}
	for _, c := range cases {
		if got := MirrorURL(c.mirror, c.raw); got != c.want {
			t.Errorf("MirrorURL(%q, %q) = %q, want %q", c.mirror, c.raw, got, c.want)
		}
	}
	set := defaultSettings()
	set.DownloadMirror = "https://ghfast.top/"
	g := mirroredGeo(set)
	if !strings.HasPrefix(g.GeoIP, "https://ghfast.top/https://github.com/") || !strings.HasPrefix(g.ASN, "https://ghfast.top/") {
		t.Fatalf("mirrored geo = %+v", g)
	}
	if strings.HasPrefix(set.Geo.GeoIP, "https://ghfast.top/") {
		t.Fatal("stored URL rewritten")
	}
	if m, err := NormalizeMirror(" https://gh-proxy.com "); err != nil || m != "https://gh-proxy.com/" {
		t.Fatalf("NormalizeMirror = %q, %v", m, err)
	}
	if _, err := NormalizeMirror("ghfast.top"); err == nil {
		t.Fatal("scheme-less mirror accepted")
	}
}

func TestMirrorAppliesToCoreAndGeo(t *testing.T) {
	h := newHarness(t, nil)
	set := h.s.Settings()
	set.DownloadMirror = "https://ghfast.top/"
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if got := h.s.mirrored(coreReleaseBase + "v1/x.gz"); got != "https://ghfast.top/"+coreReleaseBase+"v1/x.gz" {
		t.Fatalf("core URL = %q", got)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	h.prof.mu.Lock()
	in := h.prof.inputs[len(h.prof.inputs)-1]
	h.prof.mu.Unlock()
	if !strings.HasPrefix(in.Geo.MMDB, "https://ghfast.top/https://github.com/") {
		t.Fatalf("composed geo URL = %q", in.Geo.MMDB)
	}
	if strings.HasPrefix(h.s.Settings().Geo.MMDB, "https://ghfast.top/") {
		t.Fatal("stored geo URL rewritten")
	}
}

func TestTestMirrors(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/bad/") {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	old, oldProbe := MirrorPresets, mirrorProbeURL
	MirrorPresets = []string{srv.URL + "/ok/", srv.URL + "/bad/"}
	mirrorProbeURL = "https://github.com/x/y"
	t.Cleanup(func() { MirrorPresets, mirrorProbeURL = old, oldProbe })
	h := newHarness(t, nil)
	res := h.s.TestMirrors()
	if len(res) != 2 || res[0].Ms <= 0 || res[0].Error != "" || res[1].Ms != -1 || res[1].Error == "" {
		t.Fatalf("results = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(hits)
	if len(hits) != 2 || hits[0] != "HEAD /bad/https://github.com/x/y" || hits[1] != "HEAD /ok/https://github.com/x/y" {
		t.Fatalf("hits = %q", hits)
	}
}
