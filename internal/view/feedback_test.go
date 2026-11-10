package view

import (
	"testing"

	"lanflow/internal/app"
)

// TestFeedbackDialog: compose, preview everything, send only with an
// endpoint.
func TestFeedbackDialog(t *testing.T) {
	api, s, tt := newTest(t)
	s.OpenFeedback("TUN 下网页打不开")
	tt.Frame()
	if !tt.HasText("反馈问题") || !tt.HasText("预览将要发送的内容") || !tt.HasText("还没有设置上报地址：报告只能保存到文件，再自行发送。可在 设置 › 关于 › 反馈上报 中配置。") {
		t.Fatalf("compose: %q", tt.Texts())
	}
	click(t, tt, "预览将要发送的内容")
	tt.Frame()
	if !tt.HasText("报告内容") || !tt.HasText("保存到文件…") || !tt.HasText("附件：诊断包 diagnostics.tar.gz，4 个文件") {
		t.Fatalf("preview: %q", tt.Texts())
	}
	if !hasPrefix(tt, "system.txt · ") {
		t.Fatalf("attachments not listed: %q", tt.Texts())
	}
	// no endpoint: 发送 does nothing
	click(t, tt, "发送")
	tt.Frame()
	if !tt.HasText("报告内容") {
		t.Fatal("sent without an endpoint")
	}
	set := api.Settings()
	set.Feedback.Endpoint = "https://reports.example.com"
	if err := api.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	click(t, tt, "发送")
	tt.Frame()
	tt.Frame()
	if tt.HasText("报告内容") || !tt.HasText("反馈已发送，谢谢！") {
		t.Fatalf("after sending: %q", tt.Texts())
	}
	if api.FeedbackInfo().LastSentAt.IsZero() {
		t.Fatal("not sent")
	}
}

// TestCrashPrompt: a crash of the previous run asks whether to report it.
func TestCrashPrompt(t *testing.T) {
	api, s, tt := newTest(t)
	api.SimulateCrash()
	s.handleNotice(nil, app.Notice{Kind: "crash-report"})
	tt.Frame()
	if !tt.HasText("上次异常退出，是否反馈？") {
		t.Fatalf("no prompt: %q", tt.Texts())
	}
	click(t, tt, "反馈…")
	tt.Frame()
	tt.Frame()
	if !tt.HasText("预览将要发送的内容") || api.FeedbackInfo().LastCrash != nil {
		t.Fatalf("feedback dialog after the prompt: %q", tt.Texts())
	}
}

func hasPrefix(tt interface{ Texts() []string }, p string) bool {
	for _, s := range tt.Texts() {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}
