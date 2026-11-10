package settings

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Mihomo core versions in 设置 › 内核: the 「内核版本」 card (active
// version, channel, 检查更新), the list of installed versions with 使用 /
// 删除, 「安装其他版本…」 (a dialog listing the releases of a channel) and
// the update settings (自动检查 / 更新通道 / 自动安装). The work is
// API.InstallCore / UseCore / RemoveCore; progress comes from
// API.CoreDownload.

// coresUI is the page state of the version management.
type coresUI struct {
	installOpen bool
	channel     int // segmented index into channelKeys
	releases    map[string][]model.CoreRelease
	loading     bool
	loadErr     string
	busy        string // version a 使用 / 删除 job runs for ("" = none)

	// installed is API.InstalledCores (which looks at the files), read
	// again only when TopicCores or TopicCoreDownload says it changed.
	installed []model.InstalledCore
	stale     *env.Stale
}

// installedStale is the flag that has installedCores read the list again.
func (p *Page) installedStale() *env.Stale {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cores.stale == nil {
		p.cores.stale = env.Watch(p.e.API, app.TopicCores, app.TopicCoreDownload)
	}
	return p.cores.stale
}

// installedCores returns the installed versions, read again only after a
// change.
func (p *Page) installedCores() []model.InstalledCore {
	cu := &p.cores
	if p.installedStale().Take() {
		cu.installed = p.e.API.InstalledCores()
	}
	return cu.installed
}

var (
	channelNames = []string{"正式版", "预发布"}
	channelKeys  = []string{model.CoreChannelStable, model.CoreChannelAlpha}
)

// channelLabel names a channel for display.
func channelLabel(channel string) string {
	for i, k := range channelKeys {
		if k == channel {
			return i18n.T(channelNames[i])
		}
	}
	return i18n.T(channelNames[0])
}

// activeInstalled returns the entry of the core in use.
func activeInstalled(list []model.InstalledCore) (model.InstalledCore, bool) {
	for _, c := range list {
		if c.Active {
			return c, true
		}
	}
	return model.InstalledCore{}, false
}

// coreVersionSection is the 「内核版本」 card: the version in use, its
// channel, 检查更新 / 更新到, then every installed version.
func (p *Page) coreVersionSection(c *ui.Context, t *ui.Theme, s model.Settings, st model.Status) {
	e := p.e
	dl := e.API.CoreDownload()
	installed := p.installedCores()
	active, hasActive := activeInstalled(installed)
	p.mu.Lock()
	latest := p.latest
	p.mu.Unlock()
	if latest != "" && (latest == st.CoreVersion || latest == s.CoreVersion || dl.Done == latest) {
		latest = "" // the update landed
	}
	versionDesc := ""
	switch {
	case dl.Active:
		versionDesc = env.CoreDownloadText(dl)
	case dl.Error != "":
		versionDesc = dl.Error
	case latest != "":
		versionDesc = i18n.Tf("有新版本 %s", latest)
	case hasActive && active.Bundled:
		versionDesc = i18n.T("应用自带的内核")
	case hasActive:
		versionDesc = channelLabel(active.Channel) + " · " + i18n.Tf("安装于 %s", active.InstalledAt.Format("2006-01-02"))
	case s.CorePath != "":
		versionDesc = i18n.T("自定义内核文件")
	}
	e.Kit.Section(c, i18n.T("内核版本"), "", func() {
		e.Kit.Row(c, i18n.T("Mihomo 版本"), versionDesc, func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				v := st.CoreVersion
				if v == "" && hasActive {
					v = active.Version
				}
				if v == "" {
					v = "—"
				}
				ui.Text(c, v).TextColor(t.TextMuted).FontFeatures("tnum")
				if hasActive {
					if active.Bundled {
						e.Kit.Tag(c, i18n.T("内置"), "gray")
					} else {
						e.Kit.Tag(c, channelLabel(active.Channel), channelKind(active.Channel))
					}
				}
				if dl.Active {
					ui.Spinner(c).Label(i18n.T("正在更新内核")).Size(14, 14)
					return
				}
				if latest != "" {
					ui.PrimaryButton(c, i18n.Tf("更新到 %s", latest)).OnClick(func() { p.updateCore(latest) })
				}
				label := i18n.T("检查更新")
				if p.checking {
					label = i18n.T("检查中…")
				}
				ui.Button(c, label).Disabled(p.checking).OnClick(func() { p.checkUpdate(st.CoreVersion) })
			})
		})
		for _, ic := range installed {
			p.installedRow(c, t, ic, dl.Active)
		}
		e.Kit.Row(c, "", "", func() {
			ui.Button(c, i18n.T("安装其他版本…")).Disabled(dl.Active).OnClick(func() {
				p.cores.installOpen = true
				p.cores.channel = channelIndex(s.CoreChannel)
				p.loadReleases(channelKeys[p.cores.channel], false)
			})
		})
	})
}

func channelKind(channel string) string {
	if channel == model.CoreChannelAlpha {
		return "orange"
	}
	return "accent"
}

func channelIndex(channel string) int {
	for i, k := range channelKeys {
		if k == channel {
			return i
		}
	}
	return 0
}

// installedRow is one installed version: name, channel, date and size,
// with 使用 / 删除.
func (p *Page) installedRow(c *ui.Context, t *ui.Theme, ic model.InstalledCore, downloading bool) {
	e := p.e
	name := ic.Version
	if ic.Bundled {
		name = i18n.T("内置")
		if ic.Version != "" {
			name += " · " + ic.Version
		}
	}
	desc := ""
	switch {
	case ic.Bundled:
		desc = i18n.T("随应用安装")
	default:
		desc = channelLabel(ic.Channel) + " · " + ic.InstalledAt.Format("2006-01-02")
	}
	if ic.Size > 0 {
		desc += " · " + env.FmtBytes(ic.Size)
	}
	busy := p.cores.busy != "" || downloading
	e.Kit.Row(c.Key("core-"+ic.Version+boolKey(ic.Bundled)), name, desc, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ic.Active {
				e.Kit.Tag(c, i18n.T("使用中"), "green")
				return
			}
			version := ic.Version
			if ic.Bundled {
				version = ""
			}
			ui.Button(c, i18n.T("使用")).Disabled(busy).OnClick(func() { p.useCore(version) })
			if !ic.Bundled {
				ui.Button(c, i18n.T("删除")).Disabled(busy).OnClick(func() {
					e.Confirm(i18n.Tf("删除内核 %s？", ic.Version), i18n.T("内核文件会从磁盘删除；需要时可以重新安装。"), i18n.T("删除"), true, func() {
						p.coreJob(ic.Version, func() error { return e.API.RemoveCore(ic.Version) }, i18n.Tf("已删除内核 %s", ic.Version))
					})
				})
			}
		})
	})
	_ = t
}

func boolKey(b bool) string {
	if b {
		return "-bundled"
	}
	return ""
}

// useCore switches to version ("" = bundled). With the TUN helper
// installed the new binary has to be registered with it, which asks for
// administrator rights, so the user is told first.
func (p *Page) useCore(version string) {
	e := p.e
	label := version
	if label == "" {
		label = i18n.T("内置")
	}
	ok := i18n.Tf("已切换到内核 %s", label)
	if version == "" {
		ok = i18n.T("已恢复内置内核")
	}
	run := func() {
		p.coreJob(label, func() error { return e.API.UseCore(version) }, ok)
	}
	if !e.API.TUNHelperInstalled() {
		run()
		return
	}
	e.Confirm(i18n.T("切换内核"), i18n.T("已安装权限组件：切换后需要重新登记新内核，期间会请求一次管理员权限；内核会以同样的配置立即重新启动，系统代理保持不变。"), i18n.T("切换"), false, run)
}

// coreJob runs one 使用 / 删除 job, marking the list busy meanwhile.
func (p *Page) coreJob(version string, job func() error, ok string) {
	p.cores.busy = version
	p.e.Run(func() error {
		err := job()
		p.mu.Lock()
		p.cores.busy = ""
		p.mu.Unlock()
		p.installedStale().Mark() // the list changed, announced or not
		return err
	}, ok)
}

// coreUpdateSection holds the update settings.
func (p *Page) coreUpdateSection(c *ui.Context, s model.Settings) {
	e := p.e
	e.Kit.Section(c, i18n.T("内核更新"), i18n.T("每天检查一次所选通道有没有新内核；发现新版本时提醒，不会自行切换，除非打开自动安装"), func() {
		p.switchRow(c, i18n.T("自动检查更新"), i18n.T("应用设置里的「自动检查更新」关闭时，内核也不会自动检查"), s.CoreAutoCheck, func(v bool) {
			s.CoreAutoCheck = v
			p.save(s, "", nil)
		})
		opts := i18n.Options(channelNames...)
		p.selectRow(c, i18n.T("更新通道"), i18n.T("预发布（Alpha）跟随最新提交，功能更新但可能不稳定"), opts[channelIndex(s.CoreChannel)], opts, func(v string) {
			s.CoreChannel = channelKeys[0]
			for i, name := range channelNames {
				if name == i18n.KeyOf(v, channelNames) {
					s.CoreChannel = channelKeys[i]
				}
			}
			p.mu.Lock()
			p.latest = ""
			p.mu.Unlock()
			p.save(s, "", nil)
		})
		p.switchRow(c, i18n.T("自动安装更新"), i18n.T("下载、校验并切换到新版本；旧版本保留在列表中"), s.CoreAutoInstall, func(v bool) {
			s.CoreAutoInstall = v
			p.save(s, "", nil)
		})
	})
}

// loadReleases fetches the release list of channel in the background.
func (p *Page) loadReleases(channel string, refresh bool) {
	e := p.e
	p.mu.Lock()
	if p.cores.releases == nil {
		p.cores.releases = map[string][]model.CoreRelease{}
	}
	if _, ok := p.cores.releases[channel]; ok && !refresh {
		p.mu.Unlock()
		return
	}
	p.cores.loading = true
	p.cores.loadErr = ""
	p.mu.Unlock()
	e.Run(func() error {
		list, err := e.API.CoreReleases(channel, refresh)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.cores.loading = false
		if err != nil {
			p.cores.loadErr = err.Error()
			return nil // shown in the dialog, not as a toast
		}
		p.cores.releases[channel] = list
		return nil
	}, "")
}

// installDialog is 「安装其他版本…」: a channel segmented control and the
// releases of that channel with dates and notes, each with 安装.
func (p *Page) installDialog(c *ui.Context, t *ui.Theme) {
	e := p.e
	cu := &p.cores
	if !cu.installOpen {
		return
	}
	dl := e.API.CoreDownload()
	ui.Modal(c, &cu.installOpen, func() {
		ui.Column(c.Key("install-core")).Width(560).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("安装其他版本")).FontSize(15).Bold()
				ui.Spacer(c)
				ch := cu.channel
				if ui.Segmented(c.Key("core-channel"), &ch, i18n.Options(channelNames...)...).Label(i18n.T("更新通道")).Changed() {
					cu.channel = ch
					p.loadReleases(channelKeys[ch], false)
				}
			})
			channel := channelKeys[cu.channel]
			p.mu.Lock()
			list := cu.releases[channel]
			loading, loadErr := cu.loading, cu.loadErr
			p.mu.Unlock()
			if channel == model.CoreChannelAlpha {
				ui.Text(c, i18n.T("预发布版本按最新提交构建，版本号是提交哈希；适合想第一时间用上新功能的用户。")).FontSize(12).TextColor(t.TextMuted)
			}
			if dl.Active {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Label(i18n.T("正在更新内核")).Size(14, 14)
					ui.Text(c, env.CoreDownloadText(dl)).FontSize(12).TextColor(t.TextMuted).Grow(1)
				})
				if dl.Total > 0 {
					ui.Progress(c, min(1, float64(dl.Bytes)/float64(dl.Total))).Label(i18n.T("下载进度"))
				}
			} else if dl.Error != "" {
				ui.Text(c, dl.Error).FontSize(11).TextColor(t.Danger)
			}
			switch {
			case loadErr != "":
				ui.Text(c, loadErr).FontSize(12).TextColor(t.Danger)
			case loading && len(list) == 0:
				ui.Text(c, i18n.T("正在读取版本列表…")).FontSize(12).TextColor(t.TextMuted)
			case len(list) == 0:
				ui.Text(c, i18n.T("没有可用的版本")).FontSize(12).TextColor(t.TextMuted)
			default:
				ui.Scroll(c.Key("core-releases")).MaxHeight(360).Children(func() {
					ui.Column(c).Radius(8).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
						for _, r := range list {
							p.releaseRow(c, t, r, dl.Active)
						}
					})
				})
			}
			ui.Row(c).Gap(8).Children(func() {
				label := i18n.T("刷新")
				if loading {
					label = i18n.T("读取中…")
				}
				ui.Button(c, label).Disabled(loading || dl.Active).OnClick(func() { p.loadReleases(channel, true) })
				ui.Spacer(c)
				if ui.Button(c, i18n.T("关闭")).Clicked() {
					cu.installOpen = false
				}
			})
		})
	})
}

// releaseRow is one release: version, date, notes excerpt and 安装.
func (p *Page) releaseRow(c *ui.Context, t *ui.Theme, r model.CoreRelease, downloading bool) {
	e := p.e
	ui.Row(c.Key("rel-"+r.Version)).Gap(12).Padding(10, 12).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, r.Version).FontSize(13).Bold().FontFeatures("tnum")
				if !r.PublishedAt.IsZero() {
					ui.Text(c, r.PublishedAt.Local().Format("2006-01-02")).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum")
				}
				if r.Size > 0 {
					ui.Text(c, env.FmtBytes(r.Size)).FontSize(11).TextColor(t.TextMuted)
				}
			})
			if r.Notes != "" {
				ui.Text(c, r.Notes).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			}
		})
		switch {
		case r.Installed:
			e.Kit.Tag(c, i18n.T("已安装"), "green")
		default:
			ui.Button(c, i18n.T("安装")).Disabled(downloading).OnClick(func() {
				e.Run(func() error {
					err := e.API.InstallCore(r.Channel, r.Version)
					p.installedStale().Mark()
					if err != nil {
						return err
					}
					// re-read the list so the row shows 已安装 (the service
					// answers from its cache; no network)
					if list, err := e.API.CoreReleases(r.Channel, false); err == nil {
						p.mu.Lock()
						p.cores.releases[r.Channel] = list
						p.mu.Unlock()
					}
					return nil
				}, i18n.Tf("内核 %s 已安装，可在列表中点「使用」切换", r.Version))
			})
		}
	})
}
