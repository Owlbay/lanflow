package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
)

// feedbackSecrets must not appear anywhere in a report.
var feedbackSecrets = []string{
	"hk.secret-airport.example", "cdn.secret-airport.example", "0b9c5d3e", "s3cretPass", "tok123", "zzz9",
	"api/v1/client/subscribe", "8.8.4.4", "203.0.113.9", "2001:4860:4860::8844",
	"google-secret.example", "private-site.example", "MyHomeWiFi", "bearer-secret-xyz",
}

const feedbackSub = secretSub + `
hosts:
  private-site.example: 10.0.0.9
`

// reportServer is a minimal report service following docs/feedback-api.md.
type reportServer struct {
	mu       sync.Mutex
	seen     map[string]bool
	requests []*recordedReq
	plan     []int // status codes to answer first (429 gets Retry-After: 2)
}

type recordedReq struct {
	header http.Header
	meta   map[string]any
	files  map[string]string
	raw    []byte
}

func (rs *reportServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/reports" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		rec := &recordedReq{header: r.Header.Clone(), files: map[string]string{}, raw: raw}
		rs.mu.Lock()
		rs.requests = append(rs.requests, rec)
		var code int
		if len(rs.plan) > 0 {
			code, rs.plan = rs.plan[0], rs.plan[1:]
		}
		rs.mu.Unlock()
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2")
		}
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"try later"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer bearer-secret-xyz" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad token"}`))
			return
		}
		mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var metaJSON []byte
		switch mt {
		case "application/json":
			metaJSON = raw
		case "multipart/form-data":
			mr := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				switch p.FormName() {
				case "report":
					metaJSON = data
				case "diagnostics":
					gz, err := gzip.NewReader(bytes.NewReader(data))
					if err != nil {
						t.Errorf("diagnostics not gzip: %v", err)
						break
					}
					tr := tar.NewReader(gz)
					for {
						h, err := tr.Next()
						if err != nil {
							break
						}
						b, _ := io.ReadAll(tr)
						rec.files[h.Name] = string(b)
					}
				}
			}
		default:
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if err := json.Unmarshal(metaJSON, &rec.meta); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		id := r.Header.Get("X-Lanflow-Report-Id")
		if id == "" || id != rec.meta["id"] {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		rs.mu.Lock()
		dup := rs.seen[id]
		rs.seen[id] = true
		rs.mu.Unlock()
		if dup {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "url": "https://reports.example/r/" + id})
	}
}

func newReportServer(t *testing.T) (*reportServer, string) {
	rs := &reportServer{seen: map[string]bool{}}
	srv := httptest.NewServer(rs.handler(t))
	t.Cleanup(srv.Close)
	return rs, srv.URL
}

// feedbackHarness is a harness with a secret-laden subscription, logs and
// error records, and an endpoint at url ("" = none).
func feedbackHarness(t *testing.T, endpoint string) *harness {
	t.Helper()
	h := newHarness(t, nil)
	id := h.s.Status().CurrentSubscription
	if err := h.store.SetContent(id, []byte(feedbackSub)); err != nil {
		t.Fatal(err)
	}
	if err := h.s.StartCore(); err != nil {
		t.Fatal(err)
	}
	set := h.s.Settings()
	set.Bypass = append(set.Bypass, "google-secret.example")
	set.SSIDRules = []model.SSIDRule{{SSID: "MyHomeWiFi", SubscriptionID: id}}
	set.Feedback.Endpoint = endpoint
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.fb.sleep = func(context.Context, time.Duration) error { return nil }
	_ = os.WriteFile(filepath.Join(h.dir, "mihomo.log"), []byte(strings.Join([]string{
		`time="t" level=info msg="[TCP] 192.168.1.23:5000 --> google-secret.example:443 match DomainSuffix using 香港 01"`,
		`time="t" level=warning msg="[TCP] dial 香港 01 192.168.1.23:5001 --> private-site.example:443 error: timeout"`,
		`time="t" level=error msg="[Provider] extra pull error: Get https://sub.secret-airport.example/api/v1/client/subscribe?token=tok123: EOF"`,
		`time="t" level=warning msg="dns 8.8.4.4 failed, fallback 192.168.1.1"`,
	}, "\n")+"\n"), 0o600)
	h.s.alog.Errorf("subscription update failed: Get https://sub.secret-airport.example/api/v1/client/subscribe?token=tok123 via 203.0.113.9 and [2001:4860:4860::8844]:443")
	h.s.alog.Infof("resolver 8.8.4.4 ok, gateway 192.168.1.1")
	return h
}

// reportText is everything a report shows and sends.
func reportText(t *testing.T, h *harness, rep model.FeedbackReport) string {
	t.Helper()
	js, _ := json.Marshal(rep)
	b, err := h.s.builtReport(rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	var all bytes.Buffer
	all.Write(js)
	all.Write(b.body)
	if b.archive != nil {
		gz, _ := gzip.NewReader(bytes.NewReader(b.archive))
		raw, _ := io.ReadAll(gz)
		all.Write(raw)
	}
	return all.String()
}

func TestFeedbackReportRedacted(t *testing.T) {
	h := feedbackHarness(t, "")
	rep, err := h.s.BuildFeedback(model.FeedbackOptions{
		Description:        "断网了，订阅是 https://sub.secret-airport.example/api/v1/client/subscribe?token=tok123 ，出口 203.0.113.9",
		Contact:            "me@example.com",
		IncludeDiagnostics: true,
		IncludeLog:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := reportText(t, h, rep)
	for _, s := range feedbackSecrets {
		if strings.Contains(text, s) {
			i := strings.Index(text, s)
			t.Errorf("report leaks %q: …%s…", s, text[max(0, i-80):min(len(text), i+80)])
		}
	}
	// what stays: LAN addresses, the problem lines, the shape of the config
	for _, keep := range []string{"192.168.1.1", "pull error", "x.x.x.x", "<omitted:", "香港 01", "断网了"} {
		if !strings.Contains(text, keep) {
			t.Errorf("report lost %q", keep)
		}
	}
	// connection lines never come along, even redacted
	if strings.Contains(text, "-->") {
		t.Error("report carries connection lines")
	}
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestBuildFeedbackPreview(t *testing.T) {
	h := feedbackHarness(t, "")
	rep, err := h.s.BuildFeedback(model.FeedbackOptions{Description: "x", IncludeDiagnostics: true})
	if err != nil {
		t.Fatal(err)
	}
	if !uuidRe.MatchString(rep.ID) || rep.Kind != model.FeedbackManual || rep.App.Version != "dev" || rep.App.OS == "" {
		t.Fatalf("report = %+v", rep)
	}
	if len(rep.Errors) == 0 || rep.Errors[len(rep.Errors)-1].Kind != model.ErrorKindError {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	if rep.Log != "" {
		t.Fatal("log included without IncludeLog")
	}
	names := map[string]bool{}
	for _, a := range rep.Attachments {
		names[a.Name] = true
		if a.Size != int64(len(a.Content)) {
			t.Errorf("%s: size %d, content %d", a.Name, a.Size, len(a.Content))
		}
		if strings.HasPrefix(a.Name, "logs/lanflow.log") {
			t.Errorf("lanflow.log attached without IncludeLog")
		}
	}
	for _, n := range []string{"README.txt", "system.txt", "config/settings.json", "config/runtime.yaml", "logs/mihomo.log"} {
		if !names[n] {
			t.Errorf("attachments lack %s", n)
		}
	}
	b, _ := h.s.builtReport(rep.ID)
	if rep.Size != int64(len(b.body)) || !strings.HasPrefix(b.contentType, "multipart/form-data") {
		t.Fatalf("size %d vs body %d, type %s", rep.Size, len(b.body), b.contentType)
	}
	other, _ := h.s.BuildFeedback(model.FeedbackOptions{})
	if other.ID == rep.ID {
		t.Fatal("IDs repeat")
	}
	ob, _ := h.s.builtReport(other.ID)
	if ob.contentType != "application/json" || len(other.Attachments) != 0 {
		t.Fatalf("plain report: %s, %d attachments", ob.contentType, len(other.Attachments))
	}

	// save to file
	out := filepath.Join(t.TempDir(), "report.zip")
	if err := h.s.SaveFeedback(rep.ID, out); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string]bool{}
	for _, f := range zr.File {
		got[f.Name] = true
	}
	if !got["report.json"] || !got["diagnostics/system.txt"] {
		t.Fatalf("saved zip has %v", got)
	}
	if err := h.s.SaveFeedback("nope", out); err == nil {
		t.Fatal("unknown id saved")
	}
}

func TestSendFeedback(t *testing.T) {
	rs, url := newReportServer(t)
	h := feedbackHarness(t, url)
	if err := h.s.SetFeedbackToken("bearer-secret-xyz"); err != nil {
		t.Fatal(err)
	}
	if info := h.s.FeedbackInfo(); !info.TokenSet || info.Endpoint != url || info.Errors == 0 {
		t.Fatalf("info = %+v", info)
	}
	rep, err := h.s.BuildFeedback(model.FeedbackOptions{Description: "hello", Contact: "me@example.com", IncludeDiagnostics: true, IncludeLog: true})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := h.s.SendFeedback(rep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID != rep.ID || sent.Duplicate || !strings.HasSuffix(sent.URL, rep.ID) {
		t.Fatalf("sent = %+v", sent)
	}
	req := rs.requests[0]
	if req.header.Get("X-Lanflow-Version") != "dev" || req.header.Get("X-Lanflow-Report-Id") != rep.ID {
		t.Fatalf("headers = %v", req.header)
	}
	if req.meta["description"] != "hello" || req.meta["contact"] != "me@example.com" || req.meta["schema"] != float64(1) {
		t.Fatalf("meta = %v", req.meta)
	}
	if atts, _ := req.meta["attachments"].([]any); len(atts) != len(rep.Attachments) || len(req.files) != len(rep.Attachments) {
		t.Fatalf("attachments: meta %d, archive %d, report %d", len(atts), len(req.files), len(rep.Attachments))
	}
	if req.files["system.txt"] != attachment(rep, "system.txt") {
		t.Fatal("the archive differs from the preview")
	}
	// what was sent is exactly what was previewed: the token never is
	if bytes.Contains(req.raw, []byte("bearer-secret-xyz")) {
		t.Fatal("token in the body")
	}
	// sending again: the server dedupes by ID
	again, err := h.s.SendFeedback(rep.ID)
	if err != nil || !again.Duplicate {
		t.Fatalf("resend = %+v, %v", again, err)
	}
	if h.s.FeedbackInfo().LastSentAt.IsZero() {
		t.Fatal("LastSentAt not set")
	}
}

func attachment(rep model.FeedbackReport, name string) string {
	for _, a := range rep.Attachments {
		if a.Name == name {
			return a.Content
		}
	}
	return "<missing>"
}

func TestSendFeedbackRetries(t *testing.T) {
	rs, url := newReportServer(t)
	h := feedbackHarness(t, url)
	_ = h.s.SetFeedbackToken("bearer-secret-xyz")
	var waits []time.Duration
	h.s.fb.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	rs.plan = []int{http.StatusServiceUnavailable, http.StatusTooManyRequests}
	rep, _ := h.s.BuildFeedback(model.FeedbackOptions{Description: "retry"})
	if _, err := h.s.SendFeedback(rep.ID); err != nil {
		t.Fatal(err)
	}
	if len(rs.requests) != 3 {
		t.Fatalf("%d requests, want 3", len(rs.requests))
	}
	if len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("waits = %v (backoff 1 s, then Retry-After 2 s)", waits)
	}
	for _, r := range rs.requests {
		if r.header.Get("X-Lanflow-Report-Id") != rep.ID {
			t.Fatal("retries must keep the report ID")
		}
	}

	// gives up after feedbackAttempts
	rs.plan = []int{500, 500, 500, 500, 500}
	rs.requests = nil
	if _, err := h.s.SendFeedback(rep.ID); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
	if len(rs.requests) != feedbackAttempts {
		t.Fatalf("%d attempts", len(rs.requests))
	}

	// a long Retry-After is not waited for
	if d := parseRetryAfter("3600", time.Now()); d != time.Hour {
		t.Fatal(d)
	}
	// 4xx other than 429: no retry
	rs.plan = nil
	rs.requests = nil
	_ = h.s.SetFeedbackToken("wrong")
	if _, err := h.s.SendFeedback(rep.ID); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err = %v", err)
	}
	if len(rs.requests) != 1 {
		t.Fatalf("401 retried: %d requests", len(rs.requests))
	}
}

func TestSendFeedbackWithoutEndpoint(t *testing.T) {
	h := feedbackHarness(t, "")
	rep, _ := h.s.BuildFeedback(model.FeedbackOptions{Description: "x"})
	if _, err := h.s.SendFeedback(rep.ID); err == nil {
		t.Fatal("sent without an endpoint")
	}
	for _, bad := range []string{"ftp://x", "http://reports.example.com", "not a url"} {
		if _, err := feedbackURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if u, err := feedbackURL("https://r.example.workers.dev/"); err != nil || u != "https://r.example.workers.dev/v1/reports" {
		t.Fatalf("%s %v", u, err)
	}
}

func TestAutoReportSummaryOnly(t *testing.T) {
	rs, url := newReportServer(t)
	h := feedbackHarness(t, url)
	_ = h.s.SetFeedbackToken("bearer-secret-xyz")
	set := h.s.Settings()
	set.Feedback.AutoReport = true
	if err := h.s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	h.s.sendAutoReport()
	if len(rs.requests) != 1 {
		t.Fatalf("%d requests", len(rs.requests))
	}
	req := rs.requests[0]
	if req.meta["kind"] != model.FeedbackAuto || req.meta["log"] != nil || len(req.files) != 0 || req.header.Get("Content-Type") != "application/json" {
		t.Fatalf("auto report carries more than a summary: %v", req.meta)
	}
	if errs, _ := req.meta["errors"].([]any); len(errs) == 0 {
		t.Fatal("no errors in the summary")
	}
	// nothing new: nothing sent
	h.s.sendAutoReport()
	if len(rs.requests) != 1 {
		t.Fatal("the same errors were sent twice")
	}
}
