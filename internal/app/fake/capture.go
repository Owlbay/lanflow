package fake

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// The 抓包 page against sample data: a trusted root, a selection and a
// handful of requests (JSON, form, image, a passthrough, a pinned host).

type fakeCapture struct {
	mu      sync.Mutex
	init    bool
	set     model.CaptureSettings
	enabled bool
	until   time.Time
	ca      model.CAState
	flows   []model.CaptureFlowDetail
}

var fcaps sync.Map // *Fake → *fakeCapture

func (f *Fake) capInit() *fakeCapture {
	v, _ := fcaps.LoadOrStore(f, &fakeCapture{})
	c := v.(*fakeCapture)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.init {
		return c
	}
	c.init = true
	c.ca = model.CATrusted
	c.set = model.CaptureSettings{Domains: []string{"api.github.com", "+.example.com"}, AutoOffMin: -1, Acknowledged: true,
		Apps: []model.CaptureApp{{Name: "Slack", Kind: "path", Match: "/Applications/Slack.app/*"}}}
	now := time.Now()
	png := sampleIcon("/Applications/Preview.app")
	mk := func(id int64, method, host, path string, status int, ct string, req, resp []byte, proc string, flags ...string) model.CaptureFlowDetail {
		reqCT := "application/json"
		if bytes.Contains(req, []byte("grant_type=")) {
			reqCT = "application/x-www-form-urlencoded"
		}
		f := model.CaptureFlow{ID: id, Start: now.Add(time.Duration(id-20) * time.Second), DurationMs: 40 + id*7, Process: proc,
			ProcessPath: "/Applications/" + proc + ".app", Attribution: "certain", Selector: "api.github.com", Proto: "h2",
			Method: method, Scheme: "https", Host: host, Port: 443, Path: path, Status: status, ContentType: ct,
			ReqSize: int64(len(req)), RespSize: int64(len(resp)), Flags: append([]string{model.FlowDecrypted}, flags...), Done: true}
		return model.CaptureFlowDetail{Flow: f,
			ReqHeader: []model.KV{{Key: "user-agent", Value: proc + "/1.0"}, {Key: "authorization", Value: "Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
				{Key: "content-type", Value: reqCT}, {Key: "cookie", Value: "session=0123456789abcdef"}},
			RespHeader: []model.KV{{Key: "content-type", Value: ct}, {Key: "set-cookie", Value: "sid=s3cr3t; HttpOnly"}},
			ReqBody:    req, RespBody: resp, ReqBodySize: int64(len(req)), RespBodySize: int64(len(resp)),
			TLS:    model.CaptureTLS{SNI: host, Version: "TLS 1.3", Cipher: "TLS_AES_128_GCM_SHA256", ALPN: "h2"},
			Timing: model.CaptureTiming{UpstreamDialMs: 3, UpstreamTLSMs: 25, ClientTLSMs: 31, TTFBMs: 38, EndMs: 40 + id*7},
		}
	}
	c.flows = []model.CaptureFlowDetail{
		mk(1, "GET", "api.github.com", "/user?access_token=abcdef0123456789", 200, "application/json", nil,
			[]byte(`{"login":"octocat","id":1,"email":"octo@example.com","plan":{"name":"pro","private_repos":9999}}`), "Code"),
		mk(2, "POST", "api.github.com", "/graphql", 200, "application/json", []byte(`{"query":"query { viewer { login } }","variables":{}}`),
			[]byte(`{"data":{"viewer":{"login":"octocat"}}}`), "Code"),
		mk(3, "POST", "login.example.com", "/oauth/token", 200, "application/x-www-form-urlencoded",
			[]byte("grant_type=password&username=octo&password=hunter2hunter2&client_secret=abcdef123456"), []byte(`{"access_token":"eyJhbGciOiJIUzI1NiJ9.e30.x","expires_in":3600}`), "Slack"),
		mk(4, "GET", "img.example.com", "/avatar.png", 200, "image/png", nil, png, "Slack"),
		mk(5, "GET", "api.github.com", "/repos/a/b/issues", 404, "application/json", nil, []byte(`{"message":"Not Found"}`), "Code"),
	}
	pass := model.CaptureFlowDetail{Flow: model.CaptureFlow{ID: 6, Start: now, Host: "pinned.example.com", Port: 443, Proto: "tls", Process: "Slack",
		Flags: []string{model.FlowPassthrough, model.FlowPinned}, Error: "证书固定：此主机暂时跳过解密", Done: true, Selector: "+.example.com"}}
	c.flows = append(c.flows, pass)
	return c
}

func (f *Fake) CaptureStatus() model.CaptureStatus {
	c := f.capInit()
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.CaptureStatus{Enabled: c.enabled, Domains: len(c.set.Domains), Apps: len(c.set.Apps), Until: c.until, CA: c.ca,
		CAExpires: time.Now().Add(80 * 24 * time.Hour), CAName: "Lanflow Local CA (fake 2026-11-10)", AgentUp: c.enabled,
		Flows: len(c.flows), FindProcess: true, Settings: c.set}
}

func (f *Fake) CaptureFlows() []model.CaptureFlow {
	c := f.capInit()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.CaptureFlow, len(c.flows))
	for i, d := range c.flows {
		out[i] = d.Flow
	}
	return out
}

func (f *Fake) CaptureFlow(id int64) (model.CaptureFlowDetail, error) {
	c := f.capInit()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range c.flows {
		if d.Flow.ID == id {
			return d, nil
		}
	}
	return model.CaptureFlowDetail{}, errors.New("这条记录已不在抓包缓冲区中。")
}

func (f *Fake) SetCapture(on bool) error {
	c := f.capInit()
	c.mu.Lock()
	c.enabled = on
	c.until = time.Time{}
	if on {
		c.until = time.Now().Add(30 * time.Minute)
	}
	c.mu.Unlock()
	f.emit(app.TopicCapture, app.TopicStatus)
	return nil
}

func (f *Fake) ExtendCapture() error {
	c := f.capInit()
	c.mu.Lock()
	c.until = c.until.Add(30 * time.Minute)
	c.mu.Unlock()
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) SaveCaptureSettings(s model.CaptureSettings) error {
	c := f.capInit()
	c.mu.Lock()
	c.set = s
	c.mu.Unlock()
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) AddCaptureDomain(host string, enable bool) error {
	c := f.capInit()
	c.mu.Lock()
	c.set.Domains = append(c.set.Domains, host)
	c.mu.Unlock()
	if enable {
		return f.SetCapture(true)
	}
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) AddCaptureApp(path, name string, enable bool) error {
	c := f.capInit()
	c.mu.Lock()
	c.set.Apps = append(c.set.Apps, app.CaptureAppFor(path, name))
	c.mu.Unlock()
	if enable {
		return f.SetCapture(true)
	}
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) ClearCaptureFlows() error {
	c := f.capInit()
	c.mu.Lock()
	c.flows = nil
	c.mu.Unlock()
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) InstallCACert() error {
	c := f.capInit()
	c.mu.Lock()
	c.ca = model.CATrusted
	c.mu.Unlock()
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) RegenerateCA() error { return f.InstallCACert() }

func (f *Fake) RemoveCACert() error {
	c := f.capInit()
	c.mu.Lock()
	c.ca, c.enabled = model.CANone, false
	c.mu.Unlock()
	f.emit(app.TopicCapture)
	return nil
}

func (f *Fake) ExportCACert(string) error    { return nil }
func (f *Fake) ExportHAR(string, bool) error { return nil }
