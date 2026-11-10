package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// fakeSecrets is an in-memory keychain.
type fakeSecrets struct {
	mu   sync.Mutex
	m    map[string]string
	sets int
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{m: map[string]string{}} }

func (f *fakeSecrets) Get(key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[key]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return []byte(v), nil
}
func (f *fakeSecrets) Set(key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets++
	f.m[key] = string(value)
	return nil
}
func (f *fakeSecrets) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
	return nil
}
func (f *fakeSecrets) get(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[key]
}

func TestLinkSecretsGoToKeychainOnce(t *testing.T) {
	h := newHarness(t, nil)
	ks := newFakeSecrets()
	h.s.cs.secrets = ks
	subs := h.s.d.store.List()
	if err := h.s.storeLinkSecrets(subs); err != nil {
		t.Fatal(err)
	}
	if err := h.s.storeLinkSecrets(subs); err != nil {
		t.Fatal(err)
	}
	if got := ks.get(syncfile.SubscriptionKey(subs[0].ID)); got != "https://example.com/sub" || ks.sets != 1 {
		t.Fatalf("keychain = %q after %d writes", got, ks.sets)
	}
	// the sync document names it, never spells it
	doc := buildDoc(h.s.snapshotLocal(), linksKeychain)
	if len(doc.Subscriptions) != 1 || doc.Subscriptions[0].URL != syncfile.SubscriptionRef(subs[0].ID) {
		t.Fatalf("doc subscriptions = %+v", doc.Subscriptions)
	}
	// no keychain: an error to report, nothing stored
	h.s.cs.secrets = nil
	saved := Secrets
	Secrets = nil
	defer func() { Secrets = saved }()
	h.s.cs.links.stored = nil
	if err := h.s.storeLinkSecrets(subs); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("without a keychain: %v", err)
	}
}

func TestResolveLink(t *testing.T) {
	h := newHarness(t, nil)
	ks := newFakeSecrets()
	ks.m["sub-abc"] = "https://a.example/sub"
	h.s.cs.secrets = ks
	for _, c := range []struct{ ref, local, want string }{
		{"keychain:sub-abc", "", "https://a.example/sub"},
		{"keychain:sub-missing", "", ""},                                               // another machine: the user enters it
		{"keychain:sub-missing", "https://mine/sub", "https://mine/sub"},               // this machine still knows it
		{"https://plain.example/sub", "https://mine/sub", "https://plain.example/sub"}, // written by an agent
		{"", "https://mine/sub", "https://mine/sub"},
	} {
		if got := h.s.resolveLink(c.ref, c.local); got != c.want {
			t.Errorf("resolveLink(%q, %q) = %q, want %q", c.ref, c.local, got, c.want)
		}
	}
}

func TestSubscriptionWithoutLinkNeedsURL(t *testing.T) {
	h := newHarness(t, nil)
	sub, _ := h.store.Put(model.Subscription{ID: "feedbeef", Name: "远端", Source: urlSubscriptionK})
	h.s.refreshStore(TopicSubs)
	var got model.Subscription
	for _, s := range h.s.Subscriptions() {
		if s.ID == sub.ID {
			got = s
		}
	}
	if !got.NeedsURL {
		t.Fatalf("NeedsURL not set: %+v", got)
	}
	if err := h.s.UpdateSubscription(sub.ID); !errors.Is(err, errNeedsURL) {
		t.Fatalf("update without a link: %v", err)
	}
	got.URL = "https://example.com/new"
	if err := h.s.EditSubscription(got); err != nil {
		t.Fatal(err)
	}
	for _, s := range h.s.Subscriptions() {
		if s.ID == sub.ID && s.NeedsURL {
			t.Fatal("still NeedsURL after the link was entered")
		}
	}
}

// ---- sync engine ----

// syncHarness is a harness with sync driven by hand (no loop steps), an
// in-memory keychain and no debounce wait.
func syncHarness(t *testing.T) (*harness, *fakeSecrets, string) {
	t.Helper()
	h := newHarness(t, nil)
	h.s.cs.paused.Store(true)
	ks := newFakeSecrets()
	h.s.cs.secrets = ks
	h.s.cs.debounce = time.Nanosecond
	h.s.cs.home = t.TempDir()
	return h, ks, t.TempDir()
}

func readSync(t *testing.T, dir string) syncfile.Doc {
	t.Helper()
	return readDoc(t, filepath.Join(dir, syncfile.FileName))
}

// writeExternal replaces the sync file as another machine would.
func writeExternal(t *testing.T, dir string, edit func(d *syncfile.Doc)) []byte {
	t.Helper()
	d := readSync(t, dir)
	edit(&d)
	data := syncfile.Encode(d)
	if err := os.WriteFile(filepath.Join(dir, syncfile.FileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func step(h *harness) {
	time.Sleep(2 * time.Millisecond) // past the (nanosecond) debounce
	h.s.syncStep()
	time.Sleep(2 * time.Millisecond)
	h.s.syncStep()
}

func reloads(h *harness) int {
	h.ctrl.mu.Lock()
	defer h.ctrl.mu.Unlock()
	return h.ctrl.reloads
}

func TestSyncEnableWritesFileWithKeychainRefs(t *testing.T) {
	h, ks, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, syncfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "example.com/sub") {
		t.Fatalf("link written into the file:\n%s", data)
	}
	sub := h.s.Subscriptions()[0]
	if !strings.Contains(string(data), syncfile.SubscriptionRef(sub.ID)) || ks.get(syncfile.SubscriptionKey(sub.ID)) != "https://example.com/sub" {
		t.Fatalf("keychain reference missing:\n%s", data)
	}
	st := h.s.ConfigSyncStatus()
	if st.Mode != model.SyncFolder || st.Path != filepath.Join(dir, syncfile.FileName) || st.State != model.SyncStateOK || st.LastWrittenAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if h.s.Settings().Sync.Dir != dir {
		t.Fatal("sync folder not saved")
	}
}

// TestSyncOwnWritesDoNotTrigger: a local change is written to the file,
// and the poller does not take this machine's own write for an external
// change.
func TestSyncOwnWritesDoNotTrigger(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	set := h.s.Settings()
	set.Mode = model.ModeGlobal
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SaveCustomRule(model.CustomRule{Type: "DOMAIN-SUFFIX", Payload: "a.example", Target: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	h.s.cs.mu.Lock()
	h.s.cs.dirty = true
	h.s.cs.mu.Unlock()
	before := reloads(h)
	validated := len(h.core.validated)
	for i := 0; i < 3; i++ {
		step(h)
	}
	doc := readSync(t, dir)
	if doc.Settings.Mode != model.ModeGlobal || len(doc.CustomRules) != 1 {
		t.Fatalf("local change not written: mode %s rules %+v", doc.Settings.Mode, doc.CustomRules)
	}
	st := h.s.ConfigSyncStatus()
	if !st.LastAppliedAt.IsZero() || st.Summary != "" || reloads(h) != before || len(h.core.validated) != validated {
		t.Fatalf("own write was loaded back: %+v, reloads %d→%d", st, before, reloads(h))
	}
	// nothing changed: the file is not rewritten either
	fi1, _ := os.Stat(filepath.Join(dir, syncfile.FileName))
	h.s.cs.mu.Lock()
	h.s.cs.dirty = true
	h.s.cs.mu.Unlock()
	step(h)
	fi2, _ := os.Stat(filepath.Join(dir, syncfile.FileName))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("an unchanged configuration was rewritten")
	}
}

func TestSyncLoadsValidExternalChange(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	before := reloads(h)
	validated := len(h.core.validated)
	writeExternal(t, dir, func(d *syncfile.Doc) {
		d.Settings.LogLevel = "warning"
		d.Settings.AllowLAN = true
		d.CustomRules = append(d.CustomRules, syncfile.Rule{Type: "DOMAIN", Payload: "b.example", Target: "DIRECT"})
		d.Overrides = append(d.Overrides, syncfile.Override{ID: "ext1", Name: "外部", Enabled: true, Global: true, Text: "ipv6: false\n"})
	})
	step(h)
	set := h.s.Settings()
	if set.LogLevel != "warning" || !set.AllowLAN {
		t.Fatalf("settings not loaded: %+v", set)
	}
	if rules := h.s.CustomRules(); len(rules) != 1 || rules[0].Payload != "b.example" {
		t.Fatalf("rules not loaded: %+v", rules)
	}
	if list := h.s.OverrideList(); len(list) != 1 || list[0].ID != "ext1" {
		t.Fatalf("overrides not loaded: %+v", list)
	}
	if len(h.core.validated) == validated {
		t.Fatal("the change was not checked with the core")
	}
	if reloads(h) == before {
		t.Fatal("the running core was not reloaded")
	}
	st := h.s.ConfigSyncStatus()
	if st.State != model.SyncStateOK || st.LastAppliedAt.IsZero() || !strings.Contains(st.Summary, "设置") {
		t.Fatalf("status = %+v", st)
	}
	// machine-local items stay: TUN, core path
	if set.TUN || set.Sync.Dir != dir {
		t.Fatalf("machine-local items changed: %+v", set.Sync)
	}
}

func TestSyncRefusesInvalidChange(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	good, _ := os.ReadFile(path)
	port := h.s.Settings().MixedPort

	// a type error: refused with its line
	bad := strings.Replace(string(good), "mixedPort: 7890", "mixedPort: lots", 1)
	os.WriteFile(path, []byte(bad), 0o600)
	step(h)
	st := h.s.ConfigSyncStatus()
	want := syncfile.LineOf([]byte(bad), "settings.mixedPort")
	if st.State != model.SyncStateError || st.ErrorLine != want || want == 0 || !strings.Contains(st.Error, "settings.mixedPort") {
		t.Fatalf("status = %+v (want line %d)", st, want)
	}
	if h.s.Settings().MixedPort != port {
		t.Fatal("settings changed by a refused file")
	}

	// valid YAML the core refuses: nothing is stored
	h.core.mu.Lock()
	h.core.validateErr = errors.New("parse config: bad rule")
	h.core.mu.Unlock()
	os.WriteFile(path, good, 0o600)
	writeExternal(t, dir, func(d *syncfile.Doc) {
		d.Settings.LogLevel = "error"
		d.CustomRules = []syncfile.Rule{{Type: "DOMAIN", Payload: "c.example", Target: "DIRECT"}}
	})
	step(h)
	st = h.s.ConfigSyncStatus()
	if st.State != model.SyncStateError || !strings.Contains(st.Error, "bad rule") {
		t.Fatalf("status = %+v", st)
	}
	if h.s.Settings().LogLevel == "error" || len(h.s.CustomRules()) != 0 {
		t.Fatal("a configuration the core refused was loaded")
	}

	// the same refused content is not tried again; a fixed one loads
	h.core.mu.Lock()
	h.core.validateErr = nil
	h.core.mu.Unlock()
	step(h)
	if h.s.Settings().LogLevel == "error" {
		t.Fatal("refused content retried")
	}
	writeExternal(t, dir, func(d *syncfile.Doc) { d.Settings.LogLevel = "debug" })
	step(h)
	if st := h.s.ConfigSyncStatus(); h.s.Settings().LogLevel != "debug" || st.State != model.SyncStateOK || st.Error != "" {
		t.Fatalf("fixed file not loaded: %+v", st)
	}
}

func TestSyncMissingKeychainRef(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	writeExternal(t, dir, func(d *syncfile.Doc) {
		d.Subscriptions = append(d.Subscriptions, syncfile.Subscription{ID: "cafe0001", Name: "另一台的订阅", URL: syncfile.SubscriptionRef("cafe0001"), AutoUpdateHours: 24})
	})
	step(h)
	st := h.s.ConfigSyncStatus()
	if st.State != model.SyncStateOK || len(st.MissingSecrets) != 1 || st.MissingSecrets[0] != "另一台的订阅" {
		t.Fatalf("status = %+v", st)
	}
	var got model.Subscription
	for _, s := range h.s.Subscriptions() {
		if s.ID == "cafe0001" {
			got = s
		}
	}
	if !got.NeedsURL || got.AutoUpdateHours != 24 {
		t.Fatalf("subscription = %+v", got)
	}
	// the reference stays in the file; it is not dropped by the rewrite
	step(h)
	doc := readSync(t, dir)
	found := false
	for _, s := range doc.Subscriptions {
		if s.ID == "cafe0001" {
			found = true
		}
	}
	if !found {
		t.Fatal("the subscription without a link fell out of the file")
	}
}

func TestSyncConflictCopies(t *testing.T) {
	h, _, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	other := readSync(t, dir)
	other.Settings.LogLevel = "error"
	os.WriteFile(filepath.Join(dir, "lanflow 2.yaml"), syncfile.Encode(other), 0o600)
	step(h)
	st := h.s.ConfigSyncStatus()
	if len(st.Conflicts) != 1 || st.Conflicts[0] != "lanflow 2.yaml" {
		t.Fatalf("conflicts = %+v", st.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(dir, "lanflow 2.yaml")); err != nil {
		t.Fatal("a conflict copy was removed without asking")
	}
	if h.s.Settings().LogLevel == "error" {
		t.Fatal("a conflict copy was loaded without asking")
	}
	// the user keeps the copy: it is loaded, lanflow.yaml becomes it,
	// and the copy moves to the backups
	if err := h.s.ResolveConfigConflict("lanflow 2.yaml", true); err != nil {
		t.Fatal(err)
	}
	if h.s.Settings().LogLevel != "error" || readSync(t, dir).Settings.LogLevel != "error" {
		t.Fatal("kept copy not loaded")
	}
	if _, err := os.Stat(filepath.Join(dir, "lanflow 2.yaml")); !os.IsNotExist(err) {
		t.Fatal("kept copy still beside the file")
	}
	backups, _ := os.ReadDir(filepath.Join(h.dir, syncBackupDir))
	if len(backups) != 2 {
		t.Fatalf("backups = %d, want the old file and the copy", len(backups))
	}
	if st := h.s.ConfigSyncStatus(); len(st.Conflicts) != 0 {
		t.Fatalf("conflicts after resolving: %+v", st.Conflicts)
	}
	if err := h.s.ResolveConfigConflict("../settings.json", false); err == nil {
		t.Fatal("resolved a file that is not a conflict copy")
	}
}

func TestSyncICloudPlaceholderDownloads(t *testing.T) {
	h, _, _ := syncHarness(t)
	drive := syncfile.ICloudDrive(h.s.cs.home)
	dir := filepath.Join(drive, syncfile.ICloudSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := h.s.ConfigSyncStatus(); !st.ICloudAvailable || st.ICloudDir != dir {
		t.Fatalf("iCloud not detected: %+v", st)
	}
	var asked []string
	h.s.cs.download = func(p string) error { asked = append(asked, p); return nil }
	if err := h.s.SetConfigSync(model.SyncICloud, "", ""); err != nil {
		t.Fatal(err)
	}
	// another machine's file arrives as a placeholder only
	other := readSync(t, dir)
	other.Settings.LogLevel = "error"
	os.Remove(filepath.Join(dir, syncfile.FileName))
	os.WriteFile(filepath.Join(dir, syncfile.PlaceholderName(syncfile.FileName)), []byte("bplist"), 0o600)
	step(h)
	if st := h.s.ConfigSyncStatus(); st.State != model.SyncStateWaiting {
		t.Fatalf("state = %s", st.State)
	}
	if len(asked) != 1 || asked[0] != filepath.Join(dir, syncfile.FileName) {
		t.Fatalf("download requests = %q", asked)
	}
	if _, err := os.Stat(filepath.Join(dir, syncfile.FileName)); err == nil {
		t.Fatal("wrote over a file iCloud has not downloaded")
	}
	// downloaded: loaded
	os.Remove(filepath.Join(dir, syncfile.PlaceholderName(syncfile.FileName)))
	os.WriteFile(filepath.Join(dir, syncfile.FileName), syncfile.Encode(other), 0o600)
	step(h)
	if h.s.Settings().LogLevel != "error" {
		t.Fatal("downloaded file not loaded")
	}
}

func TestSyncEnableAsksBeforeReplacing(t *testing.T) {
	h, _, dir := syncHarness(t)
	// another machine's file is already there
	other := buildDoc(h.s.snapshotLocal(), linksKeychain)
	other.Settings.LogLevel = "error"
	other.Settings.MixedPort = 7999
	path := filepath.Join(dir, syncfile.FileName)
	os.WriteFile(path, syncfile.Encode(other), 0o600)
	orig, _ := os.ReadFile(path)

	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); !errors.Is(err, ErrConfigSyncChoice) {
		t.Fatalf("want ErrConfigSyncChoice, got %v", err)
	}
	if h.s.Settings().Sync.Mode != model.SyncOff {
		t.Fatal("sync turned on without the choice")
	}
	if now, _ := os.ReadFile(path); string(now) != string(orig) {
		t.Fatal("file touched before the choice")
	}
	// use this machine's configuration: the file is replaced, a backup kept
	if err := h.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseLocal); err != nil {
		t.Fatal(err)
	}
	if d := readSync(t, dir); d.Settings.MixedPort != 7890 {
		t.Fatalf("file not replaced: %d", d.Settings.MixedPort)
	}
	if b, _ := os.ReadDir(filepath.Join(h.dir, syncBackupDir)); len(b) != 1 {
		t.Fatal("no backup of the replaced file")
	}
	// and the other way round: the file wins
	h.s.SetConfigSync(model.SyncOff, "", "")
	os.WriteFile(path, orig, 0o600)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, model.SyncUseFile); err != nil {
		t.Fatal(err)
	}
	if set := h.s.Settings(); set.MixedPort != 7999 || set.LogLevel != "error" {
		t.Fatalf("file not used: %d %s", set.MixedPort, set.LogLevel)
	}
}

func TestApplyConfigFile(t *testing.T) {
	h, _, dir := syncHarness(t)
	src := buildDoc(h.s.snapshotLocal(), linksPlain) // a hand-made file with a plain link
	src.Settings.LogLevel = "warning"
	src.CustomRules = []syncfile.Rule{{Type: "DOMAIN-SUFFIX", Payload: "agent.example", Target: "DIRECT"}}
	path := filepath.Join(dir, "edited.yaml")
	os.WriteFile(path, syncfile.Encode(src), 0o600)

	res, err := h.s.ApplyConfigFile(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.Diff) == 0 || h.s.Settings().LogLevel == "warning" || len(h.s.CustomRules()) != 0 {
		t.Fatalf("dry run applied or empty: %+v", res)
	}
	for _, l := range res.Diff {
		if strings.Contains(l.Text, "example.com/sub") {
			t.Fatal("the diff reveals a subscription link")
		}
	}
	if !strings.Contains(strings.Join(res.Changed, ","), "settings") || !strings.Contains(strings.Join(res.Changed, ","), "customRules") {
		t.Fatalf("changed = %v", res.Changed)
	}
	res, err = h.s.ApplyConfigFile(path, false)
	if err != nil || !res.Applied {
		t.Fatalf("apply: %+v %v", res, err)
	}
	if h.s.Settings().LogLevel != "warning" || len(h.s.CustomRules()) != 1 {
		t.Fatal("not applied")
	}
	// again: nothing to do
	if res, _ := h.s.ApplyConfigFile(path, false); res.Applied || len(res.Changed) != 0 {
		t.Fatalf("second apply: %+v", res)
	}
	// errors carry the line
	bad := strings.Replace(string(syncfile.Encode(src)), "payload: agent.example", "payload: agent.example\n    target2: x", 1)
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := h.s.ApplyConfigFile(path, false); err != nil {
		t.Fatalf("unknown key must only warn: %v", err)
	}
	bad = strings.Replace(string(syncfile.Encode(src)), "type: DOMAIN-SUFFIX", "type: NOT-A-RULE", 1)
	os.WriteFile(path, []byte(bad), 0o600)
	_, err = h.s.ApplyConfigFile(path, false)
	if want := syncfile.LineOf([]byte(bad), "customRules[0]"); err == nil || want == 0 || !strings.Contains(err.Error(), fmt.Sprintf("第 %d 行", want)) {
		t.Fatalf("invalid rule: %v (want line %d)", err, want)
	}
	if _, err := h.s.ApplyConfigFile("relative.yaml", true); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := h.s.ApplyConfigFile("", true); err == nil {
		t.Fatal("empty path without sync accepted")
	}
}

// TestSyncPlainLinkMovesToKeychain: a link written into the file by hand
// is used, stored in the keychain, and the file rewritten with the
// reference.
func TestSyncPlainLinkMovesToKeychain(t *testing.T) {
	h, ks, dir := syncHarness(t)
	if err := h.s.SetConfigSync(model.SyncFolder, dir, ""); err != nil {
		t.Fatal(err)
	}
	id := h.s.Subscriptions()[0].ID
	writeExternal(t, dir, func(d *syncfile.Doc) { d.Subscriptions[0].URL = "https://new.example/sub?token=t0k" })
	step(h)
	step(h)
	if got, _ := h.store.Get(id); got.URL != "https://new.example/sub?token=t0k" {
		t.Fatalf("link not used: %q", got.URL)
	}
	if ks.get(syncfile.SubscriptionKey(id)) != "https://new.example/sub?token=t0k" {
		t.Fatal("link not stored in the keychain")
	}
	data, _ := os.ReadFile(filepath.Join(dir, syncfile.FileName))
	if strings.Contains(string(data), "t0k") || !strings.Contains(string(data), syncfile.SubscriptionRef(id)) {
		t.Fatalf("file not rewritten with the reference:\n%s", data)
	}
	// the rewrite is this machine's own: not loaded again
	applied := h.s.ConfigSyncStatus().LastAppliedAt
	step(h)
	if !h.s.ConfigSyncStatus().LastAppliedAt.Equal(applied) {
		t.Fatal("own rewrite loaded as an external change")
	}
}
