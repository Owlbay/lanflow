package fake

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 反馈问题 in the fake: made-up error records, reports kept in memory,
// sending succeeds when an endpoint is set.

type fakeFeedback struct {
	mu      sync.Mutex
	token   string
	crashed bool // LANFLOW_FAKE_CRASH=1 shows the crash prompt
	reports map[string]model.FeedbackReport
	sent    time.Time
}

var fb = &fakeFeedback{crashed: os.Getenv("LANFLOW_FAKE_CRASH") == "1"}

func fakeErrors() []model.ErrorRecord {
	now := time.Now()
	plat := runtime.GOOS + "/" + runtime.GOARCH
	code := 1
	return []model.ErrorRecord{
		{Time: now.Add(-2 * time.Hour), Kind: model.ErrorKindError, Message: `subscription update "机场" failed: Get "https://sub.example.com/***": timeout`, Version: "0.1.1", Platform: plat},
		{Time: now.Add(-40 * time.Minute), Kind: model.ErrorKindCoreExit, Message: "Mihomo 内核意外退出（exit status 1）", Detail: `time="…" level=fatal msg="Parse config error: proxy 0: missing type"`, ExitCode: &code, Version: "0.1.1", Platform: plat},
	}
}

func (f *Fake) FeedbackInfo() model.FeedbackInfo {
	set := f.Settings().Feedback
	fb.mu.Lock()
	defer fb.mu.Unlock()
	info := model.FeedbackInfo{Endpoint: set.Endpoint, AutoReport: set.AutoReport, TokenSet: fb.token != "", Errors: len(fakeErrors()), LastSentAt: fb.sent}
	if fb.crashed {
		r := model.ErrorRecord{Time: time.Now().Add(-time.Hour), Kind: model.ErrorKindPanic, Message: "panic in main: runtime error: index out of range [3] with length 3", Detail: "goroutine 1 [running]:\nmain.main()\n\t/src/main.go:42", Version: "0.1.1", Platform: runtime.GOOS + "/" + runtime.GOARCH}
		info.LastCrash = &r
	}
	return info
}

func (f *Fake) SetFeedbackToken(token string) error {
	f.wait()
	fb.mu.Lock()
	fb.token = strings.TrimSpace(token)
	fb.mu.Unlock()
	f.emit(app.TopicSettings)
	return nil
}

func (f *Fake) DismissLastCrash() {
	fb.mu.Lock()
	fb.crashed = false
	fb.mu.Unlock()
	f.emit(app.TopicSettings)
}

func (f *Fake) BuildFeedback(o model.FeedbackOptions) (model.FeedbackReport, error) {
	f.wait()
	rep := model.FeedbackReport{
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1e12),
		Kind:        model.FeedbackManual,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		Description: strings.TrimSpace(o.Description),
		Contact:     strings.TrimSpace(o.Contact),
		App:         model.FeedbackApp{Version: "0.1.1", OS: runtime.GOOS, Arch: runtime.GOARCH, Core: "v1.19.0", Language: f.Settings().Language},
		Errors:      fakeErrors(),
		Endpoint:    f.Settings().Feedback.Endpoint,
	}
	if o.IncludeLog {
		rep.Log = "2026-11-10 10:00:00.000 INFO    start core: ok\n2026-11-10 10:05:00.000 ERROR   notice [error] 订阅更新失败\n"
	}
	if o.IncludeDiagnostics {
		for _, a := range []model.FeedbackFile{
			{Name: "README.txt", Content: "Lanflow feedback diagnostics\n"},
			{Name: "system.txt", Content: "os:                    " + runtime.GOOS + "\ncore:                  running\n"},
			{Name: "config/settings.json", Content: "{\n  \"MixedPort\": 7890,\n  \"Bypass\": \"<omitted: 3 items>\"\n}\n"},
			{Name: "logs/mihomo.log", Content: "time=\"…\" level=warning msg=\"[DNS] resolve failed\"\n"},
		} {
			a.Size = int64(len(a.Content))
			rep.Attachments = append(rep.Attachments, a)
		}
	}
	rep.Size = int64(len(rep.Description) + len(rep.Log) + 600)
	for _, a := range rep.Attachments {
		rep.Size += a.Size / 3
	}
	fb.mu.Lock()
	if fb.reports == nil {
		fb.reports = map[string]model.FeedbackReport{}
	}
	fb.reports[rep.ID] = rep
	fb.mu.Unlock()
	return rep, nil
}

func (f *Fake) SendFeedback(id string) (model.FeedbackSent, error) {
	f.wait()
	fb.mu.Lock()
	_, ok := fb.reports[id]
	fb.mu.Unlock()
	if !ok {
		return model.FeedbackSent{}, errors.New(i18n.T("找不到这份反馈报告，请重新生成。"))
	}
	if strings.TrimSpace(f.Settings().Feedback.Endpoint) == "" {
		return model.FeedbackSent{}, errors.New(i18n.T("还没有设置上报地址：请在 设置 › 关于 › 反馈上报 中填写，或把报告保存到文件后手动发送。"))
	}
	fb.mu.Lock()
	fb.sent = time.Now()
	fb.mu.Unlock()
	f.emit(app.TopicSettings)
	return model.FeedbackSent{ID: id}, nil
}

func (f *Fake) SaveFeedback(id, path string) error {
	f.wait()
	fb.mu.Lock()
	_, ok := fb.reports[id]
	fb.mu.Unlock()
	if !ok {
		return errors.New(i18n.T("找不到这份反馈报告，请重新生成。"))
	}
	return os.WriteFile(path, []byte("PK\x05\x06"+strings.Repeat("\x00", 18)), 0o600)
}
