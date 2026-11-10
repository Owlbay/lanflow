package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 反馈问题 (docs/feedback-api.md): BuildFeedback assembles a report from
// the user's description, the recent error records and, when chosen, the
// redacted diagnostics bundle and the recent part of lanflow.log; the
// window shows it in full, then SendFeedback POSTs exactly that to
// {endpoint}/v1/reports, or SaveFeedback writes it to a file. Nothing is
// uploaded without the user's confirmation, except, when the user turns it
// on, a summary of new error records (no diagnostics, no log).

const (
	feedbackSchema      = 1
	feedbackPath        = "/v1/reports"
	feedbackMaxBody     = 8 << 20 // docs/feedback-api.md: the server accepts at most 10 MB
	feedbackDefaultErrs = 50
	feedbackMaxErrs     = 200
	feedbackMaxDesc     = 10000
	feedbackMaxContact  = 200
	feedbackKeep        = 8 // built reports kept for Send/Save
	feedbackAttempts    = 4
	feedbackMaxWait     = 60 * time.Second // longest Retry-After honored
	feedbackTokenKey    = "feedback-token"
	feedbackTokenFile   = "feedback.secret"

	autoReportDelay = 30 * time.Second // gathers a burst of errors into one report
	autoReportGap   = 10 * time.Minute // at most one automatic report per gap
	autoReportErrs  = 20
)

// feedbackState is the state of 反馈问题; its own lock.
type feedbackState struct {
	mu        sync.Mutex
	lastCrash *model.ErrorRecord // the previous run's crash, until answered
	reports   []*builtReport     // newest last, at most feedbackKeep
	lastSent  time.Time

	token       string
	tokenLoaded bool
	tokenFile   bool // no keychain: the obfuscated file holds it

	autoTimer *time.Timer
	autoAfter time.Time // records after this are not reported yet
	autoLast  time.Time // last automatic report attempt
	autoInit  bool

	sleep func(context.Context, time.Duration) error // tests
}

// builtReport is a report with its request body, ready to send or save.
type builtReport struct {
	rep         model.FeedbackReport
	meta        []byte // the JSON metadata (wireReport)
	archive     []byte // diagnostics.tar.gz, nil without attachments
	body        []byte
	contentType string
}

// wireReport is the JSON metadata the server receives
// (docs/feedback-api.md §请求体).
type wireReport struct {
	Schema      int                 `json:"schema"`
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	CreatedAt   time.Time           `json:"createdAt"`
	Description string              `json:"description"`
	Contact     string              `json:"contact,omitempty"`
	App         model.FeedbackApp   `json:"app"`
	Errors      []model.ErrorRecord `json:"errors"`
	Log         string              `json:"log,omitempty"`
	Attachments []wireFile          `json:"attachments"`
}

type wireFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// FeedbackInfo implements API.FeedbackInfo.
func (s *Service) FeedbackInfo() model.FeedbackInfo {
	set := s.Settings().Feedback
	tok, _ := s.feedbackToken()
	count := s.elog.Count()
	s.fb.mu.Lock()
	defer s.fb.mu.Unlock()
	info := model.FeedbackInfo{
		Endpoint:   set.Endpoint,
		AutoReport: set.AutoReport,
		TokenSet:   tok != "",
		Errors:     count,
		LastSentAt: s.fb.lastSent,
	}
	if c := s.fb.lastCrash; c != nil {
		cc := *c
		info.LastCrash = &cc
	}
	return info
}

// DismissLastCrash implements API.DismissLastCrash.
func (s *Service) DismissLastCrash() {
	s.fb.mu.Lock()
	s.fb.lastCrash = nil
	s.fb.mu.Unlock()
	s.em.emit(TopicSettings)
}

// BuildFeedback implements API.BuildFeedback.
func (s *Service) BuildFeedback(o model.FeedbackOptions) (model.FeedbackReport, error) {
	r, err := s.buildReport(model.FeedbackManual, o, nil)
	if err != nil {
		return model.FeedbackReport{}, err
	}
	s.alog.Infof("feedback report %s built (%d errors, %d attachments, %d bytes)", r.rep.ID, len(r.rep.Errors), len(r.rep.Attachments), len(r.body))
	return r.rep, nil
}

// buildReport assembles a report; errs, when not nil, replaces the recent
// error records (the automatic report).
func (s *Service) buildReport(kind string, o model.FeedbackOptions, errs []model.ErrorRecord) (*builtReport, error) {
	set := s.Settings()
	st := s.Status()
	n := o.Errors
	if n <= 0 {
		n = feedbackDefaultErrs
	}
	n = min(n, feedbackMaxErrs)
	if errs == nil {
		errs = s.elog.Recent(n)
	}
	for i := range errs {
		errs[i].Message = redactReport(errs[i].Message)
		errs[i].Detail = redactReport(errs[i].Detail)
	}
	if errs == nil {
		errs = []model.ErrorRecord{}
	}
	rep := model.FeedbackReport{
		ID:          newUUID(),
		Kind:        kind,
		CreatedAt:   s.d.now().UTC().Truncate(time.Second),
		Description: redactReport(clipRunes(strings.TrimSpace(o.Description), feedbackMaxDesc)),
		Contact:     clipRunes(strings.TrimSpace(o.Contact), feedbackMaxContact),
		App: model.FeedbackApp{
			Version:   s.version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
			OSVersion: osVersion(),
			Core:      st.CoreVersion,
			Language:  set.Language,
		},
		Errors:   errs,
		Endpoint: strings.TrimSpace(set.Feedback.Endpoint),
	}
	if o.IncludeLog {
		if data, err := readTail(s.alog.Path(), feedbackTailBytes/2); err == nil {
			rep.Log = redactReport(string(data))
		}
	}
	if o.IncludeDiagnostics {
		for _, f := range s.diagFiles(true, o.IncludeLog) {
			rep.Attachments = append(rep.Attachments, model.FeedbackFile{Name: f.name, Size: int64(len(f.data)), Content: string(f.data)})
		}
	}
	b, err := encodeReport(rep)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("生成反馈报告失败：%v"), err)
	}
	if len(b.body) > feedbackMaxBody {
		return nil, fmt.Errorf(i18n.T("反馈报告太大（%s），请取消勾选日志或诊断信息后重试。"), fmtBytes(int64(len(b.body))))
	}
	b.rep.Size = int64(len(b.body))
	s.fb.mu.Lock()
	s.fb.reports = append(s.fb.reports, b)
	if len(s.fb.reports) > feedbackKeep {
		s.fb.reports = s.fb.reports[len(s.fb.reports)-feedbackKeep:]
	}
	s.fb.mu.Unlock()
	return b, nil
}

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// encodeReport makes the request body: JSON alone without attachments,
// else multipart/form-data with the JSON as "report" and the files as
// "diagnostics" (diagnostics.tar.gz).
func encodeReport(rep model.FeedbackReport) (*builtReport, error) {
	w := wireReport{
		Schema: feedbackSchema, ID: rep.ID, Kind: rep.Kind, CreatedAt: rep.CreatedAt,
		Description: rep.Description, Contact: rep.Contact, App: rep.App, Errors: rep.Errors, Log: rep.Log,
		Attachments: []wireFile{},
	}
	for _, f := range rep.Attachments {
		w.Attachments = append(w.Attachments, wireFile{Name: f.Name, Size: f.Size})
	}
	meta, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	b := &builtReport{rep: rep, meta: meta}
	if len(rep.Attachments) == 0 {
		b.body, b.contentType = meta, "application/json"
		return b, nil
	}
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for _, f := range rep.Attachments {
		if err := tw.WriteHeader(&tar.Header{Name: f.Name, Mode: 0o600, Size: int64(len(f.Content)), ModTime: rep.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := io.WriteString(tw, f.Content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	b.archive = tgz.Bytes()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="report"; filename="report.json"`)
	h.Set("Content-Type", "application/json")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	_, _ = pw.Write(meta)
	h = textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="diagnostics"; filename="diagnostics.tar.gz"`)
	h.Set("Content-Type", "application/gzip")
	if pw, err = mw.CreatePart(h); err != nil {
		return nil, err
	}
	_, _ = pw.Write(b.archive)
	if err := mw.Close(); err != nil {
		return nil, err
	}
	b.body, b.contentType = body.Bytes(), mw.FormDataContentType()
	return b, nil
}

func (s *Service) builtReport(id string) (*builtReport, error) {
	s.fb.mu.Lock()
	defer s.fb.mu.Unlock()
	for _, r := range s.fb.reports {
		if r.rep.ID == id {
			return r, nil
		}
	}
	return nil, errors.New(i18n.T("找不到这份反馈报告，请重新生成。"))
}

// SaveFeedback implements API.SaveFeedback: a zip with report.json, the
// log and the diagnostics files, for sending by hand.
func (s *Service) SaveFeedback(id, path string) (err error) {
	defer func() { s.logOp("save feedback report "+filepath.Base(path), err) }()
	if strings.TrimSpace(path) == "" {
		return errors.New(i18n.T("没有选择保存位置。"))
	}
	r, err := s.builtReport(id)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		if w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: r.rep.CreatedAt}); err == nil {
			_, _ = w.Write(data)
		}
	}
	var pretty bytes.Buffer
	if json.Indent(&pretty, r.meta, "", "  ") == nil {
		add("report.json", pretty.Bytes())
	} else {
		add("report.json", r.meta)
	}
	for _, f := range r.rep.Attachments {
		add("diagnostics/"+f.Name, []byte(f.Content))
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf(i18n.T("生成反馈报告失败：%v"), err)
	}
	if err := writeFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf(i18n.T("写入 %s 失败：%v"), path, err)
	}
	return nil
}

// SendFeedback implements API.SendFeedback.
func (s *Service) SendFeedback(id string) (sent model.FeedbackSent, err error) {
	defer func() { s.logOp("send feedback report "+id, err) }()
	r, err := s.builtReport(id)
	if err != nil {
		return sent, err
	}
	sent, err = s.postReport(s.rootCtx, r)
	if err != nil {
		return sent, err
	}
	s.fb.mu.Lock()
	s.fb.lastSent = s.d.now()
	s.fb.mu.Unlock()
	s.em.emit(TopicSettings)
	return sent, nil
}

// feedbackURL is {endpoint}/v1/reports.
func feedbackURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errors.New(i18n.T("还没有设置上报地址：请在 设置 › 关于 › 反馈上报 中填写，或把报告保存到文件后手动发送。"))
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf(i18n.T("上报地址无效：%s"), endpoint)
	}
	if u.Scheme == "http" && !isLocalHost(u.Hostname()) {
		return "", errors.New(i18n.T("上报地址必须使用 https。"))
	}
	return strings.TrimRight(u.String(), "/") + feedbackPath, nil
}

func isLocalHost(h string) bool {
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

// postReport sends a built report, retrying with backoff: network errors
// and 5xx answers wait 1, 2, 4 s; 429 waits what Retry-After says (at
// most feedbackMaxWait). 201 is a new report, 200 one the server had.
func (s *Service) postReport(ctx context.Context, r *builtReport) (model.FeedbackSent, error) {
	target, err := feedbackURL(r.rep.Endpoint)
	if err != nil {
		// the endpoint may have been set after the report was built
		r.rep.Endpoint = s.Settings().Feedback.Endpoint
		if target, err = feedbackURL(r.rep.Endpoint); err != nil {
			return model.FeedbackSent{}, err
		}
	}
	token, err := s.feedbackToken()
	if err != nil {
		return model.FeedbackSent{}, err
	}
	var last error
	wait := time.Second
	viaProxy := false
	for attempt := 0; attempt < feedbackAttempts; attempt++ {
		if attempt > 0 {
			if err := s.fbSleep(ctx, wait); err != nil {
				return model.FeedbackSent{}, err
			}
			wait *= 2
		}
		client := s.d.httpDirect(30 * time.Second)
		if viaProxy {
			client = s.d.httpVia(s.Settings().MixedPort, 30*time.Second)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(r.body))
		if err != nil {
			return model.FeedbackSent{}, err
		}
		req.Header.Set("Content-Type", r.contentType)
		req.Header.Set("User-Agent", "Lanflow/"+s.version())
		req.Header.Set("X-Lanflow-Version", s.version())
		req.Header.Set("X-Lanflow-Report-Id", r.rep.ID)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return model.FeedbackSent{}, ctx.Err()
			}
			last = fmt.Errorf(i18n.T("连接上报服务器失败：%v"), err)
			// a blocked endpoint may be reachable through the proxy
			viaProxy = !viaProxy && s.Status().Core == model.CoreRunning
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		var ans struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &ans)
		switch {
		case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
			sent := model.FeedbackSent{ID: ans.ID, URL: ans.URL, Duplicate: resp.StatusCode == http.StatusOK}
			if sent.ID == "" {
				sent.ID = r.rep.ID
			}
			return sent, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			d := parseRetryAfter(resp.Header.Get("Retry-After"), s.d.now())
			if d > feedbackMaxWait {
				return model.FeedbackSent{}, fmt.Errorf(i18n.T("上报服务器繁忙，请在 %s 后重试。"), d.Round(time.Second))
			}
			if d > 0 {
				wait = d
			}
			last = errors.New(i18n.T("上报服务器繁忙（429）。"))
		case resp.StatusCode >= 500:
			last = fmt.Errorf(i18n.T("上报服务器出错（%d）：%s"), resp.StatusCode, serverError(ans.Error, data))
		default:
			return model.FeedbackSent{}, fmt.Errorf(i18n.T("上报服务器拒绝了报告（%d）：%s"), resp.StatusCode, serverError(ans.Error, data))
		}
	}
	return model.FeedbackSent{}, last
}

func serverError(msg string, body []byte) string {
	if msg != "" {
		return msg
	}
	t := strings.TrimSpace(string(body))
	if t == "" {
		return "-"
	}
	return clipRunes(t, 200)
}

// parseRetryAfter reads a Retry-After header: seconds or an HTTP date.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}

func (s *Service) fbSleep(ctx context.Context, d time.Duration) error {
	s.fb.mu.Lock()
	fn := s.fb.sleep
	s.fb.mu.Unlock()
	if fn != nil {
		return fn(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- token ----

func (s *Service) feedbackTokenPath() string { return filepath.Join(s.opt.DataDir, feedbackTokenFile) }

// feedbackToken reads the token once: from the keychain, or the file
// fallback where the system has none.
func (s *Service) feedbackToken() (string, error) {
	s.fb.mu.Lock()
	defer s.fb.mu.Unlock()
	if s.fb.tokenLoaded {
		return s.fb.token, nil
	}
	if Secrets != nil {
		v, err := Secrets.Get(feedbackTokenKey)
		switch {
		case err == nil:
			s.fb.token, s.fb.tokenLoaded = string(v), true
			return s.fb.token, nil
		case errors.Is(err, ErrSecretNotFound):
			s.fb.tokenLoaded = true
			return "", nil
		case errors.Is(err, errors.ErrUnsupported):
		default:
			return "", fmt.Errorf(i18n.T("读取钥匙串失败：%v"), err)
		}
	}
	s.fb.tokenFile = true
	data, err := os.ReadFile(s.feedbackTokenPath())
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf(i18n.T("读取密码文件失败：%v"), err)
	}
	s.fb.token, s.fb.tokenLoaded = deobfuscate(strings.TrimSpace(string(data))), true
	return s.fb.token, nil
}

// SetFeedbackToken implements API.SetFeedbackToken: "" removes it.
func (s *Service) SetFeedbackToken(token string) (err error) {
	defer func() { s.logOp("set feedback token", err) }()
	token = strings.TrimSpace(token)
	if _, err := s.feedbackToken(); err != nil {
		return err
	}
	s.fb.mu.Lock()
	file := s.fb.tokenFile
	s.fb.mu.Unlock()
	if !file {
		if token == "" {
			err = Secrets.Delete(feedbackTokenKey)
			if errors.Is(err, ErrSecretNotFound) {
				err = nil
			}
		} else {
			err = Secrets.Set(feedbackTokenKey, []byte(token))
		}
		if err != nil {
			return fmt.Errorf(i18n.T("写入钥匙串失败：%v"), err)
		}
	} else if token == "" {
		if err := os.Remove(s.feedbackTokenPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf(i18n.T("删除密码文件失败：%v"), err)
		}
	} else if err := writeFileAtomic(s.feedbackTokenPath(), []byte(obfuscate(token)+"\n"), 0o600); err != nil {
		return fmt.Errorf(i18n.T("保存密码文件失败：%v"), err)
	}
	s.fb.mu.Lock()
	s.fb.token, s.fb.tokenLoaded = token, true
	s.fb.mu.Unlock()
	s.em.emit(TopicSettings)
	return nil
}

// ---- automatic report ----

// kickAutoReport is called after every new error record: with
// Settings.Feedback.AutoReport on and an endpoint set, the new records are
// sent as a summary after autoReportDelay, at most once per autoReportGap.
func (s *Service) kickAutoReport() {
	set := s.Settings().Feedback
	if !set.AutoReport || strings.TrimSpace(set.Endpoint) == "" || s.rootCtx.Err() != nil {
		return
	}
	s.fb.mu.Lock()
	defer s.fb.mu.Unlock()
	if s.fb.autoTimer != nil {
		return
	}
	delay := autoReportDelay
	if next := s.fb.autoLast.Add(autoReportGap); !s.fb.autoLast.IsZero() && next.After(s.d.now().Add(delay)) {
		delay = next.Sub(s.d.now())
	}
	s.fb.autoTimer = time.AfterFunc(delay, func() {
		if !s.enterBackground() {
			return
		}
		defer s.wg.Done()
		defer s.elog.CatchPanic("auto report")
		s.fb.mu.Lock()
		s.fb.autoTimer = nil
		s.fb.mu.Unlock()
		s.sendAutoReport()
	})
}

// sendAutoReport sends the error records not reported yet. Failures are
// logged as warnings: an error here would record a new error and loop.
func (s *Service) sendAutoReport() {
	set := s.Settings().Feedback
	if !set.AutoReport || strings.TrimSpace(set.Endpoint) == "" {
		return
	}
	s.fb.mu.Lock()
	s.initAutoLocked()
	after := s.fb.autoAfter
	s.fb.autoLast = s.d.now()
	s.fb.mu.Unlock()
	errs := s.elog.Since(after)
	if len(errs) == 0 {
		return
	}
	if len(errs) > autoReportErrs {
		errs = errs[len(errs)-autoReportErrs:]
	}
	newest := errs[len(errs)-1].Time
	r, err := s.buildReport(model.FeedbackAuto, model.FeedbackOptions{}, errs)
	if err != nil {
		s.alog.Warnf("automatic feedback report: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(s.rootCtx, 3*time.Minute)
	defer cancel()
	if _, err := s.postReport(ctx, r); err != nil {
		s.alog.Warnf("automatic feedback report %s: %v", r.rep.ID, err)
		return
	}
	s.alog.Infof("automatic feedback report %s sent (%d errors)", r.rep.ID, len(errs))
	s.fb.mu.Lock()
	if newest.After(s.fb.autoAfter) {
		s.fb.autoAfter = newest
	}
	s.fb.lastSent = s.d.now()
	s.fb.mu.Unlock()
}

// initAutoLocked starts the automatic report at this run's start (and
// the previous run's crash).
func (s *Service) initAutoLocked() {
	if s.fb.autoInit {
		return
	}
	s.fb.autoInit = true
	s.fb.autoAfter = s.started
	if c := s.fb.lastCrash; c != nil && c.Time.Before(s.fb.autoAfter) {
		s.fb.autoAfter = c.Time.Add(-time.Nanosecond)
	}
}
