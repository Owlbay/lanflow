package settings

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Updater is what the 关于 section needs from mygo.Updater (docs/design.md
// §9 应用自动更新, MyGo 签名更新); tests replace it with SetUpdater.
type Updater interface {
	// Enabled reports whether this build can update itself: it was built
	// with an update source (publicKey + github/url in mygo.json) and can
	// write where it is installed.
	Enabled() bool
	// Check returns the newer version, or nil when the app is up to date.
	Check(ctx context.Context) (Update, error)
}

// Update is a newer version of the app, ready to download.
type Update interface {
	Version() string
	// Install downloads the update, checks its signature and replaces the
	// app; progress gets the bytes downloaded so far and the total.
	Install(ctx context.Context, progress func(downloaded, total int64)) error
}

// mygoUpdater is the Updater over mygo.Updater.
type mygoUpdater struct{}

func (mygoUpdater) Enabled() bool { return mygo.Updater.Enabled() }

func (mygoUpdater) Check(ctx context.Context) (Update, error) {
	u, err := mygo.Updater.Check(ctx)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, nil
	}
	return mygoUpdate{u}, nil
}

type mygoUpdate struct{ u *mygo.Update }

func (m mygoUpdate) Version() string { return m.u.Version }
func (m mygoUpdate) Install(ctx context.Context, progress func(downloaded, total int64)) error {
	return m.u.Install(ctx, progress)
}

// updateState is the 关于 section's update flow, written by jobs under p.mu.
type updateState struct {
	checking   bool
	found      Update // newer version known, nil = none
	installing bool
	done       int64
	total      int64
	installed  bool   // downloaded and in place; takes effect at the next launch
	err        string // last failure, shown under the row
}

// SetUpdater replaces the app updater and the relaunch call (tests).
func (p *Page) SetUpdater(u Updater, relaunch func()) {
	p.upd, p.relaunch = u, relaunch
}

// UpdatesEnabled reports whether the build can update itself.
func (p *Page) UpdatesEnabled() bool { return p.upd.Enabled() }

// AppVersion is the version shown in 关于: the packaged app's, else the
// one compiled in.
func AppVersion() string {
	if v := mygo.App.Version(); v != "" {
		return v
	}
	return appVersion
}

// CheckAppUpdate looks for a newer version off the UI thread. A manual
// check (quiet false) toasts the outcome; a background one only calls
// found with the version when there is one. Errors of a quiet check are
// kept out of the way: the user did not ask.
func (p *Page) CheckAppUpdate(quiet bool, found func(version string)) {
	e := p.e
	p.mu.Lock()
	if p.update.checking || p.update.installing {
		p.mu.Unlock()
		return
	}
	p.update.checking, p.update.err = true, ""
	p.mu.Unlock()
	e.Run(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		up, err := p.upd.Check(ctx)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.update.checking = false
		if err != nil {
			if quiet {
				return nil
			}
			if err == mygo.ErrUpdatesDisabled {
				p.update.err = i18n.T("此版本未配置更新源")
				return nil
			}
			return fmt.Errorf(i18n.T("检查更新失败：%v。当前版本不受影响，可稍后再试。"), err)
		}
		if up == nil {
			p.update.found = nil
			if !quiet {
				p.toast = i18n.T("已是最新版本")
			}
			return nil
		}
		p.update.found = up
		if quiet {
			if found != nil {
				found(up.Version())
			}
		} else {
			p.toast = i18n.Tf("有新版本 %s，点击「下载并重启」安装", up.Version())
		}
		return nil
	}, "")
}

// installAppUpdate downloads the found version and restarts the app.
func (p *Page) installAppUpdate() {
	e := p.e
	p.mu.Lock()
	up := p.update.found
	if up == nil || p.update.installing {
		p.mu.Unlock()
		return
	}
	p.update.installing, p.update.done, p.update.total, p.update.err = true, 0, 0, ""
	p.mu.Unlock()
	e.Run(func() error {
		err := up.Install(context.Background(), func(done, total int64) {
			p.mu.Lock()
			p.update.done, p.update.total = done, total
			p.mu.Unlock()
			e.Redraw()
		})
		p.mu.Lock()
		p.update.installing = false
		if err != nil {
			p.update.err = i18n.Tf("更新失败：%s。当前版本不受影响。", err.Error())
			p.mu.Unlock()
			return nil
		}
		p.update.installed = true
		p.mu.Unlock()
		if p.relaunch != nil {
			p.relaunch()
		}
		return nil
	}, "")
}

// updateRows draws the 应用更新 rows of 关于.
func (p *Page) updateRows(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	p.mu.Lock()
	u := p.update
	p.mu.Unlock()
	if !p.upd.Enabled() {
		e.Kit.Row(c, i18n.T("应用更新"), i18n.T("此版本未配置更新源"), func() {
			ui.Button(c, i18n.T("检查更新")).Disabled(true).Tooltip(i18n.T("这个构建没有更新源，无法自动更新"))
		})
		return
	}
	desc := i18n.T("每天启动时自动检查一次")
	if s.DisableUpdateCheck {
		desc = i18n.T("不会自动检查，可随时手动检查")
	}
	switch {
	case u.installed:
		desc = i18n.T("新版本已下载，重新打开澜流后生效")
	case u.installing:
		if u.total > 0 {
			desc = fmt.Sprintf(i18n.T("正在下载 %s，%d%%"), u.found.Version(), u.done*100/u.total)
		} else {
			desc = i18n.T("正在下载 ") + u.found.Version() + "…"
		}
	case u.found != nil:
		desc = i18n.Tf("有新版本 %s，当前为 %s", u.found.Version(), AppVersion())
	case u.checking:
		desc = i18n.T("正在检查更新…")
	}
	e.Kit.Row(c, i18n.T("应用更新"), desc, func() {
		ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				switch {
				case u.installed:
					ui.PrimaryButton(c, i18n.T("重新启动")).OnClick(func() {
						if p.relaunch != nil {
							p.relaunch()
						}
					})
				case u.installing:
					v := -1.0
					if u.total > 0 {
						v = float64(u.done) / float64(u.total)
					}
					ui.Progress(c, v).Width(160).Label(i18n.T("下载更新"))
				case u.found != nil:
					ui.PrimaryButton(c, i18n.T("下载并重启")).OnClick(p.installAppUpdate)
				default:
					if u.checking {
						ui.Spinner(c).Size(14, 14).Label(i18n.T("正在检查更新"))
					}
					ui.Button(c, i18n.T("检查更新")).Disabled(u.checking).OnClick(func() { p.CheckAppUpdate(false, nil) })
				}
			})
			if u.err != "" {
				ui.Text(c, u.err).FontSize(11).TextColor(t.Danger).MaxWidth(320).TextAlign(ui.End)
			}
		})
	})
	p.switchRow(c, i18n.T("自动检查更新"), i18n.T("每天启动时检查一次，有新版本时提示"), !s.DisableUpdateCheck, func(on bool) {
		s.DisableUpdateCheck = !on
		p.save(s, "", nil)
	})
}
