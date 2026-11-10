package i18n

// English of 反馈问题: the service's messages (internal/app/feedback.go,
// errlog.go) and the dialog and settings (internal/view/feedback.go).
func init() {
	for k, v := range map[string]string{
		// internal/app/errlog.go, feedback.go
		"上次异常退出，是否反馈？":                "Lanflow quit unexpectedly last time. Report it?",
		"生成反馈报告失败：%v":                 "Failed to build the feedback report: %v",
		"反馈报告太大（%s），请取消勾选日志或诊断信息后重试。": "The feedback report is too large (%s). Leave out the log or the diagnostics and try again.",
		"找不到这份反馈报告，请重新生成。":            "This feedback report is no longer available; build it again.",
		"还没有设置上报地址：请在 设置 › 关于 › 反馈上报 中填写，或把报告保存到文件后手动发送。": "No report endpoint is set: enter one in Settings › About › Feedback reporting, or save the report to a file and send it yourself.",
		"上报地址无效：%s":          "Invalid report endpoint: %s",
		"上报地址必须使用 https。":    "The report endpoint must use https.",
		"连接上报服务器失败：%v":       "Could not reach the report server: %v",
		"上报服务器繁忙，请在 %s 后重试。": "The report server is busy; try again in %s.",
		"上报服务器繁忙（429）。":      "The report server is busy (429).",
		"上报服务器出错（%d）：%s":     "The report server failed (%d): %s",
		"上报服务器拒绝了报告（%d）：%s":  "The report server refused the report (%d): %s",
	} {
		en[k] = v
	}
}
