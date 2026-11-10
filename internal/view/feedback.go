// 反馈问题 (docs/feedback-api.md): the dialog that builds a report, shows
// everything it would send, and sends it or saves it to a file; and the
// prompt after a crash of the previous run. Opened from 设置 › 关于, the
// tray menu and the crash prompt.

package view

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// feedbackDialog is the state of the 反馈问题 dialog.
type feedbackDialog struct {
	open     bool
	session  int // keys the inputs, so a new dialog starts empty
	desc     string
	contact  string
	withDiag bool
	withLog  bool
	busy     string // "build", "send", "save" while a job runs
	err      string
	report   *model.FeedbackReport // the preview; nil while composing
	preview  string                // the report's JSON as sent
	expanded map[string]*bool      // attachments opened in the preview

	crash     *model.ErrorRecord // the crash prompt
	crashOpen bool
}

// OpenFeedback opens the 反馈问题 dialog (Env.OpenFeedback); safe from any
// goroutine. prefill, when not empty, starts the description.
func (s *Shell) OpenFeedback(prefill string) {
	s.post(func() {
		f := &s.fb
		if !f.open {
			f.session++
			f.desc, f.contact, f.err = prefill, "", ""
			f.withDiag, f.withLog = true, false
			f.report, f.preview, f.expanded = nil, "", nil
		}
		f.open = true
	})
	s.ShowWindow()
}

// crashNotice is the "crash-report" notice: ask whether to report.
func (s *Shell) crashNotice() {
	info := s.api.FeedbackInfo()
	if info.LastCrash == nil {
		return
	}
	s.fb.crash, s.fb.crashOpen = info.LastCrash, true
}

// fbJob runs one feedback action off the UI thread; done runs on the UI
// thread with its outcome.
func (s *Shell) fbJob(kind string, job func() error, done func(err error)) {
	s.fb.busy, s.fb.err = kind, ""
	if s.opts.Sync { // tests
		err := job()
		s.fb.busy = ""
		if err != nil {
			s.fb.err = err.Error()
		}
		if done != nil {
			done(err)
		}
		return
	}
	go func() {
		err := job()
		s.post(func() {
			s.fb.busy = ""
			if err != nil {
				s.fb.err = err.Error()
			}
			if done != nil {
				done(err)
			}
		})
	}()
}

// previewJSON is the report as the server receives it, attachments listed
// by name and size (their text is shown below it).
func previewJSON(rep model.FeedbackReport) string {
	type file struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	w := struct {
		ID          string              `json:"id"`
		Kind        string              `json:"kind"`
		CreatedAt   time.Time           `json:"createdAt"`
		Description string              `json:"description"`
		Contact     string              `json:"contact,omitempty"`
		App         model.FeedbackApp   `json:"app"`
		Errors      []model.ErrorRecord `json:"errors"`
		Log         string              `json:"log,omitempty"`
		Attachments []file              `json:"attachments"`
	}{rep.ID, rep.Kind, rep.CreatedAt, rep.Description, rep.Contact, rep.App, rep.Errors, rep.Log, []file{}}
	for _, a := range rep.Attachments {
		w.Attachments = append(w.Attachments, file{a.Name, a.Size})
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func endpointHost(ep string) string {
	if u, err := url.Parse(strings.TrimSpace(ep)); err == nil && u.Host != "" {
		return u.Host
	}
	return ep
}

// feedbackDialogs draws the crash prompt and the 反馈问题 dialog.
func (s *Shell) feedbackDialogs(c *ui.Context) {
	t := c.Theme()
	k := s.env.Kit
	f := &s.fb
	if f.crash != nil {
		cr := f.crash
		ui.Modal(c, &f.crashOpen, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, i18n.T("上次异常退出，是否反馈？")).FontSize(15).FontWeight(600)
				})
				ui.Text(c, i18n.T("澜流上次运行时意外退出。反馈时会附上崩溃记录，发送前可以查看全部内容。")).FontSize(13)
				ui.Text(c, cr.Message).Font("monospace").FontSize(12).Padding(8, 10).Radius(6).
					Background(t.Surface).Border(1, t.Border).Selectable()
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("不用了")).Clicked() {
						f.crashOpen = false
					}
					if ui.PrimaryButton(c, i18n.T("反馈…")).Clicked() {
						f.crashOpen = false
						s.OpenFeedback(i18n.Tf("上次澜流意外退出（%s）。", cr.Time.Local().Format("2006-01-02 15:04")))
					}
				})
			})
		})
		if !f.crashOpen {
			f.crash = nil
			if s.opts.Sync {
				s.api.DismissLastCrash()
			} else {
				go s.api.DismissLastCrash()
			}
		}
	}
	if !f.open {
		return
	}
	info := s.api.FeedbackInfo()
	ui.Modal(c, &f.open, func() {
		ui.Column(c.Key(fmt.Sprint("feedback-", f.session))).Width(600).Gap(12).Children(func() {
			ui.Text(c, i18n.T("反馈问题")).FontSize(15).FontWeight(600)
			if f.report == nil {
				s.feedbackCompose(c, t, info)
			} else {
				s.feedbackPreview(c, t, info)
			}
		})
	})
}

func (s *Shell) feedbackCompose(c *ui.Context, t *ui.Theme, info model.FeedbackInfo) {
	f := &s.fb
	k := s.env.Kit
	busy := f.busy != ""
	ui.Text(c, i18n.T("说说遇到了什么问题。发送前会完整展示将要上传的内容，确认后才会发出。")).FontSize(12).TextColor(t.TextMuted)
	ui.TextArea(c.Key("fb-desc"), &f.desc).Lines(4, 10).Placeholder(i18n.T("例如：开启 TUN 后部分网站打不开，重启内核后恢复")).Label(i18n.T("问题描述")).ReadOnly(busy)
	ui.TextInput(c.Key("fb-contact"), &f.contact).Placeholder(i18n.T("邮箱或其他联系方式（可选）")).Label(i18n.T("联系方式")).ReadOnly(busy)
	ui.Column(c).Gap(6).Children(func() {
		ui.Checkbox(c.Key("fb-diag"), &f.withDiag, i18n.T("附带诊断信息：脱敏后的设置与配置、内核的警告和错误、网络状态")).Disabled(busy)
		ui.Checkbox(c.Key("fb-log"), &f.withLog, i18n.T("附带最近的澜流运行日志（已脱敏）")).Disabled(busy)
		note := i18n.T("还没有错误记录")
		if info.Errors > 0 {
			note = i18n.Tf("最近的错误记录（至多 %d 条）会一并附上", 50)
		}
		ui.Text(c, note).FontSize(12).TextColor(t.TextMuted)
	})
	ui.Text(c, i18n.T("节点地址、密码、订阅链接和令牌会被去除，公网 IP 会被遮盖；不包含连接明细和访问过的域名。")).FontSize(12).TextColor(t.TextMuted)
	if strings.TrimSpace(info.Endpoint) == "" {
		k.Banner(c, "info", i18n.T("还没有设置上报地址：报告只能保存到文件，再自行发送。可在 设置 › 关于 › 反馈上报 中配置。"), nil)
	}
	if f.err != "" {
		ui.Text(c, f.err).FontSize(12).TextColor(t.Danger).Selectable()
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if busy {
			ui.Spinner(c).Label(i18n.T("正在生成报告")).Size(14, 14)
		}
		ui.Spacer(c)
		if ui.Button(c, i18n.T("取消")).Clicked() {
			f.open = false
		}
		empty := strings.TrimSpace(f.desc) == ""
		if ui.PrimaryButton(c, i18n.T("预览将要发送的内容")).Disabled(busy || empty).Clicked() {
			opts := model.FeedbackOptions{Description: f.desc, Contact: f.contact, IncludeDiagnostics: f.withDiag, IncludeLog: f.withLog}
			var rep model.FeedbackReport
			s.fbJob("build", func() (err error) {
				rep, err = s.api.BuildFeedback(opts)
				return err
			}, func(err error) {
				if err == nil {
					f.report, f.preview, f.expanded = &rep, previewJSON(rep), map[string]*bool{}
				}
			})
		}
	})
}

func (s *Shell) feedbackPreview(c *ui.Context, t *ui.Theme, info model.FeedbackInfo) {
	f := &s.fb
	rep := f.report
	busy := f.busy != ""
	where := i18n.T("未设置上报地址，只能保存到文件")
	if info.Endpoint != "" {
		where = i18n.Tf("将发送到 %s", endpointHost(info.Endpoint))
	}
	ui.Text(c, i18n.Tf("报告 %s · 共 %s · %s", rep.ID, fmtSize(rep.Size), where)).FontSize(12).TextColor(t.TextMuted).Selectable()
	ui.Scroll(c).MaxHeight(420).Children(func() {
		ui.Column(c).Gap(10).Children(func() {
			ui.Text(c, i18n.T("报告内容")).FontSize(13).FontWeight(600)
			ui.Text(c, f.preview).Font("monospace").FontSize(11).Padding(8, 10).Radius(6).
				Background(t.Surface).Border(1, t.Border).Selectable()
			if len(rep.Attachments) > 0 {
				ui.Text(c, i18n.Tf("附件：诊断包 diagnostics.tar.gz，%d 个文件", len(rep.Attachments))).FontSize(13).FontWeight(600)
				for _, a := range rep.Attachments {
					open := f.expanded[a.Name]
					if open == nil {
						open = new(bool)
						f.expanded[a.Name] = open
					}
					ui.Collapsible(c.Key("fb-att-"+a.Name), fmt.Sprintf("%s · %s", a.Name, fmtSize(a.Size)), open, func() {
						ui.Text(c, a.Content).Font("monospace").FontSize(11).Padding(8, 10).Radius(6).
							Background(t.Surface).Border(1, t.Border).Selectable()
					})
				}
			}
		})
	})
	if f.err != "" {
		ui.Text(c, f.err).FontSize(12).TextColor(t.Danger).Selectable()
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		switch f.busy {
		case "send":
			ui.Spinner(c).Label(i18n.T("正在发送")).Size(14, 14)
		case "save":
			ui.Spinner(c).Label(i18n.T("正在保存")).Size(14, 14)
		}
		if ui.Button(c, i18n.T("返回修改")).Disabled(busy).Clicked() {
			f.report, f.preview, f.err = nil, "", ""
		}
		ui.Spacer(c)
		if ui.Button(c, i18n.T("保存到文件…")).Disabled(busy).Clicked() {
			id := rep.ID
			go s.saveFeedback(id)
		}
		if ui.PrimaryButton(c, i18n.T("发送")).Disabled(busy || info.Endpoint == "").Clicked() {
			id := rep.ID
			var sent model.FeedbackSent
			s.fbJob("send", func() (err error) {
				sent, err = s.api.SendFeedback(id)
				return err
			}, func(err error) {
				if err != nil {
					return
				}
				f.open = false
				msg := i18n.T("反馈已发送，谢谢！")
				if sent.Duplicate {
					msg = i18n.T("这份反馈之前已发送过")
				}
				detail := i18n.Tf("报告 ID：%s", sent.ID)
				if sent.URL != "" {
					detail += "\n" + sent.URL
				}
				s.toast(toastReq{msg: msg, detail: detail})
			})
		}
	})
}

// saveFeedback asks where to save the report and writes it there.
func (s *Shell) saveFeedback(id string) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       i18n.T("保存反馈报告"),
		Message:     i18n.T("报告与预览中的内容相同，可以自行发送给开发者。"),
		DefaultPath: "lanflow-feedback-" + time.Now().Format("20060102-1504") + ".zip",
		Filters:     []mygo.FileFilter{{Name: i18n.T("ZIP 压缩包"), Extensions: []string{"zip"}}},
	})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".zip"
	}
	s.post(func() {
		s.fbJob("save", func() error { return s.api.SaveFeedback(id, path) }, func(err error) {
			if err != nil {
				return
			}
			mygo.Shell.ShowItemInFolder(path)
			s.toast(toastReq{msg: i18n.Tf("已保存到 %s", filepath.Base(path))})
		})
	})
}
