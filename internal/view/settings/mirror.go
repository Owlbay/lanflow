package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 下载镜像 (Settings.DownloadMirror), in 设置 › 内核: a select of the
// preset GitHub mirrors (app.MirrorPresets) with a custom entry, and
// 「测速」, which HEAD-requests a GitHub link through every preset. The
// mirror applies to the core download and to the geo database links the
// core is given; the stored links are not rewritten.

const (
	mirrorDirect = "直连 GitHub"
	mirrorCustom = "自定义…"
)

func mirrorLabel(m string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(m, "https://"), "http://"), "/")
}

func (p *Page) mirrorSection(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	opts := []string{i18n.T(mirrorDirect)}
	for _, m := range app.MirrorPresets {
		opts = append(opts, mirrorLabel(m))
	}
	opts = append(opts, i18n.T(mirrorCustom))
	cur := opts[0]
	custom := s.DownloadMirror != "" || p.f2.mirrorCustomMode
	for i, m := range app.MirrorPresets {
		if m == s.DownloadMirror {
			cur, custom = opts[i+1], false
		}
	}
	if custom {
		cur = opts[len(opts)-1]
	}
	e.Kit.Section(c, i18n.T("下载镜像"), i18n.T("镜像站由第三方运营，澜流不对其内容负责；无法直连 GitHub 时再使用"), func() {
		e.Kit.Row(c, i18n.T("GitHub 镜像"), i18n.T("用于下载内核和地理数据库；改动后，地理数据库的镜像在下次重新加载配置时生效"), func() {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if custom {
						f := &p.f2.mirrorCustom
						if p.f2.mirrorErr == "" {
							f.show(s.DownloadMirror)
						}
						in := ui.TextInput(c.Key("mirror-custom"), &f.text).Width(240).Font("monospace").FontSize(12).
							Placeholder("https://ghfast.top/").Label(i18n.T("镜像地址")).Invalid(p.f2.mirrorErr != "")
						if in.Changed() {
							p.f2.mirrorErr = ""
						}
						if f.commit(in) {
							p.setMirror(s, f.text)
						}
					}
					v := cur
					if ui.Select(c.Key("mirror"), &v, opts).Label(i18n.T("GitHub 镜像")).Changed() {
						switch v {
						case opts[0]:
							p.f2.mirrorCustomMode = false
							p.setMirror(s, "")
						case opts[len(opts)-1]:
							// Shows the field; nothing is saved until a URL is committed.
							p.f2.mirrorCustomMode = true
							p.f2.mirrorCustom.text, p.f2.mirrorCustom.editing = "", true
						default:
							p.f2.mirrorCustomMode = false
							for i, o := range opts {
								if o == v && i >= 1 && i-1 < len(app.MirrorPresets) {
									p.setMirror(s, app.MirrorPresets[i-1])
								}
							}
						}
					}
				})
				if p.f2.mirrorErr != "" {
					ui.Text(c, p.f2.mirrorErr).FontSize(11).TextColor(t.Danger).MaxWidth(360).TextAlign(ui.End)
				}
			})
		})
		e.Kit.Row(c, i18n.T("镜像测速"), i18n.T("直连访问每个镜像站，看哪一个最快"), func() {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				label := i18n.T("测速")
				if p.f2.mirrorTesting {
					label = i18n.T("测速中…")
				}
				ui.Button(c, label).Disabled(p.f2.mirrorTesting).OnClick(func() { p.testMirrors() })
				for i, r := range p.f2.mirrorResults {
					r := r
					ui.Row(c.Key("mirror-result-" + strconv.Itoa(i))).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, mirrorLabel(r.URL)).FontSize(12).TextColor(t.TextMuted)
						e.Kit.Delay(c, r.Ms, false)
						if r.Error != "" && r.Ms < 0 {
							ui.Text(c, r.Error).FontSize(11).TextColor(t.Danger).SingleLine().MaxWidth(200)
						}
						if r.Ms > 0 && r.URL != s.DownloadMirror {
							ui.Button(c, i18n.T("使用")).Disabled(p.saving).OnClick(func() { p.setMirror(s, r.URL) })
						}
					})
				}
			})
		})
	})
}

// setMirror validates and saves a mirror prefix ("" = direct).
func (p *Page) setMirror(s model.Settings, m string) {
	m, err := app.NormalizeMirror(m)
	if err != nil {
		p.f2.mirrorErr = err.Error()
		return
	}
	p.f2.mirrorErr = ""
	if m == s.DownloadMirror {
		return
	}
	s.DownloadMirror = m
	msg := i18n.T("已关闭下载镜像，直连 GitHub")
	if m != "" {
		msg = i18n.T("下载镜像已改为 ") + mirrorLabel(m)
	}
	p.save(s, msg, nil)
}

// testMirrors runs the HEAD tests off the UI thread.
func (p *Page) testMirrors() {
	e := p.e
	p.f2.mirrorTesting = true
	e.Run(func() error {
		res := e.API.TestMirrors()
		p.mu.Lock()
		p.f2.mirrorResults, p.f2.mirrorTesting = res, false
		p.mu.Unlock()
		best := ""
		for _, r := range res {
			if r.Ms > 0 && (best == "" || r.Ms < bestMs(res, best)) {
				best = r.URL
			}
		}
		p.mu.Lock()
		if best == "" {
			p.toast = i18n.T("所有镜像站都无法访问")
		} else {
			p.toast = i18n.Tf("最快的镜像：%s", mirrorLabel(best))
		}
		p.mu.Unlock()
		return nil
	}, "")
}

func bestMs(res []model.MirrorTest, u string) int {
	for _, r := range res {
		if r.URL == u {
			return r.Ms
		}
	}
	return 0
}
