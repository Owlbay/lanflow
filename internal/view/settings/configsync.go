package settings

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// 配置同步 (docs/config-sync.md), in 设置 › 常规: where lanflow.yaml
// lives (关闭 / iCloud 云盘 / 自定义文件夹), the file and its state, and
// banners for a refused change (with the line), conflict copies (the
// user picks one, nothing is deleted) and links this machine's keychain
// lacks. Turning sync on beside a different file asks which side wins
// (the 合并 dialog); it is never decided silently.

// syncUI is the section's state; the fields set by jobs are under p.mu.
type syncUI struct {
	busy   bool
	choice *syncChoice // the 合并 dialog, nil when closed
}

// syncChoice is a pending SetConfigSync that needs the user's choice.
type syncChoice struct {
	mode, dir string
	open      bool
}

func (p *Page) configSyncSection(c *ui.Context, t *ui.Theme) {
	e := p.e
	st := e.API.ConfigSyncStatus()
	p.mu.Lock()
	busy := p.sync.busy
	p.mu.Unlock()
	e.Kit.Section(c, i18n.T("配置同步"), i18n.T("把设置、覆写、自定义规则和订阅保存为 lanflow.yaml，放进会同步的文件夹，在多台设备间保持一致；订阅链接留在钥匙串中"), func() {
		off, icloud, folder := i18n.T("关闭"), i18n.T("iCloud 云盘"), i18n.T("自定义文件夹…")
		opts := []string{off}
		if st.ICloudAvailable || st.Mode == model.SyncICloud {
			opts = append(opts, icloud)
		}
		opts = append(opts, folder)
		cur := off
		switch st.Mode {
		case model.SyncICloud:
			cur = icloud
		case model.SyncFolder:
			cur = folder
		}
		desc := i18n.T("关闭时设置只保存在本机")
		if !st.ICloudAvailable && e.Mac {
			desc = i18n.T("没有找到 iCloud 云盘；可以选择 OneDrive、坚果云等同步盘里的文件夹")
		}
		e.Kit.Row(c, i18n.T("同步位置"), desc, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				v := cur
				if ui.Select(c.Key("sync-mode"), &v, opts).Label(i18n.T("同步位置")).Disabled(busy).Changed() {
					switch v {
					case off:
						p.setSync(model.SyncOff, "", "")
					case icloud:
						p.setSync(model.SyncICloud, "", "")
					case folder:
						go p.pickSyncFolder()
					}
				}
			})
		})
		if st.Path == "" {
			return
		}
		e.Kit.Row(c, i18n.T("同步文件"), syncStateText(st), func() {
			ui.Row(c).Gap(8).Children(func() {
				if st.Mode == model.SyncFolder {
					if ui.Button(c, i18n.T("更改…")).Disabled(busy).Clicked() {
						go p.pickSyncFolder()
					}
				}
				ui.Button(c, revealLabel(e)).OnClick(func() { mygo.Shell.ShowItemInFolder(st.Path) })
			})
		})
		p.syncBanners(c, st, busy)
	})
	p.syncChoiceDialog(c, t)
}

// syncStateText is the 同步文件 row's description.
func syncStateText(st model.ConfigSyncStatus) string {
	parts := []string{st.Path}
	switch {
	case st.State == model.SyncStateWaiting:
		parts = append(parts, i18n.T("正在等待 iCloud 下载文件…"))
	case !st.LastAppliedAt.IsZero() && st.LastAppliedAt.After(st.LastWrittenAt):
		parts = append(parts, i18n.Tf("上次加载：%s", env.FmtDateTime(st.LastAppliedAt.Local())))
	case !st.LastWrittenAt.IsZero():
		parts = append(parts, i18n.Tf("上次写入：%s", env.FmtDateTime(st.LastWrittenAt.Local())))
	}
	return strings.Join(parts, "\n")
}

// syncBanners shows what needs the user: a refused change, conflict
// copies, missing links.
func (p *Page) syncBanners(c *ui.Context, st model.ConfigSyncStatus, busy bool) {
	e := p.e
	if st.Error != "" {
		msg := i18n.Tf("同步文件的修改没有加载，配置保持不变：%s", st.Error)
		if st.ErrorLine > 0 && !strings.Contains(st.Error, i18n.Tf("第 %d 行", st.ErrorLine)) {
			msg = i18n.Tf("同步文件第 %d 行的修改没有加载，配置保持不变：%s", st.ErrorLine, st.Error)
		}
		e.Kit.Banner(c, "error", msg, func() {
			ui.Button(c, revealLabel(e)).OnClick(func() { mygo.Shell.ShowItemInFolder(st.Path) })
		})
	}
	for _, name := range st.Conflicts {
		name := name
		e.Kit.Banner(c, "warn", i18n.Tf("发现冲突副本「%s」：两台设备同时修改了配置。请选择保留哪一个，另一个会移到备份文件夹。", name), func() {
			ui.Button(c, i18n.T("保留当前文件")).Disabled(busy).OnClick(func() {
				e.Confirm(i18n.T("保留当前的 lanflow.yaml？"), i18n.Tf("冲突副本「%s」会移到数据目录的 sync-backups 文件夹，不会被删除。", name), i18n.T("保留当前文件"), false, func() {
					p.syncJob(func() error { return e.API.ResolveConfigConflict(name, false) }, i18n.T("已保留当前文件"))
				})
			})
			ui.Button(c, i18n.T("保留此副本")).Disabled(busy).OnClick(func() {
				e.Confirm(i18n.Tf("改用冲突副本「%s」？", name), i18n.T("副本中的配置校验通过后会加载到本机并成为 lanflow.yaml；当前文件会移到数据目录的 sync-backups 文件夹。"), i18n.T("使用副本"), false, func() {
					p.syncJob(func() error { return e.API.ResolveConfigConflict(name, true) }, i18n.T("已改用冲突副本"))
				})
			})
		})
	}
	if n := len(st.MissingSecrets); n > 0 {
		e.Kit.Banner(c, "info", i18n.Tf("%d 个订阅的链接不在本机钥匙串中（%s），请在「订阅」页编辑并重新输入链接。", n, strings.Join(st.MissingSecrets, i18n.T("、"))), func() {
			ui.Button(c, i18n.T("前往订阅")).OnClick(func() { e.Navigate(env.Subs, "") })
		})
	}
	if st.WebDAVPasswordMissing {
		e.Kit.Banner(c, "info", i18n.T("同步文件中设置了 WebDAV 密码，但本机钥匙串里没有；需要备份到 WebDAV 时请重新输入密码。"), nil)
	}
}

// setSync calls SetConfigSync off the UI thread; a folder with a
// different lanflow.yaml opens the 合并 dialog.
func (p *Page) setSync(mode, dir, choice string) {
	e := p.e
	p.syncJob(func() error {
		err := e.API.SetConfigSync(mode, dir, choice)
		if errors.Is(err, app.ErrConfigSyncChoice) {
			p.mu.Lock()
			p.sync.choice = &syncChoice{mode: mode, dir: dir, open: true}
			p.mu.Unlock()
			return nil
		}
		return err
	}, syncOK(mode, choice))
}

func syncOK(mode, choice string) string {
	switch {
	case mode == model.SyncOff:
		return i18n.T("配置同步已关闭；同步文件保留在原处")
	case choice == model.SyncUseFile:
		return i18n.T("已开启配置同步并加载了文件中的配置")
	}
	return ""
}

// syncJob runs job with the section marked busy.
func (p *Page) syncJob(job func() error, ok string) {
	p.mu.Lock()
	p.sync.busy = true
	p.mu.Unlock()
	p.e.Run(func() error {
		err := job()
		p.mu.Lock()
		p.sync.busy = false
		p.mu.Unlock()
		return err
	}, ok)
}

// pickSyncFolder asks for a folder (the system dialog blocks: own
// goroutine) and turns folder sync on there.
func (p *Page) pickSyncFolder() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:             i18n.T("选择同步文件夹"),
		Message:           i18n.T("选择 OneDrive、坚果云、Dropbox 等同步盘里的文件夹；澜流会在其中保存 lanflow.yaml。"),
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return
	}
	p.setSync(model.SyncFolder, filepath.Clean(paths[0]), "")
}

// syncChoiceDialog asks which configuration wins when sync is turned on
// beside a different lanflow.yaml.
func (p *Page) syncChoiceDialog(c *ui.Context, t *ui.Theme) {
	p.mu.Lock()
	ch := p.sync.choice
	p.mu.Unlock()
	if ch == nil {
		return
	}
	if !ch.open {
		p.mu.Lock()
		p.sync.choice = nil
		p.mu.Unlock()
		return
	}
	ui.Modal(c, &ch.open, func() {
		ui.Column(c.Key("sync-choice")).Width(460).Gap(12).Children(func() {
			ui.Text(c, i18n.T("同步文件夹中已有配置")).FontSize(15).Bold()
			ui.Text(c, i18n.T("文件夹里的 lanflow.yaml 与本机配置不同，可能来自另一台设备。请选择保留哪一份：")).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, i18n.T("· 使用文件中的配置：校验通过后加载到本机，替换本机的设置、覆写、自定义规则和订阅（TUN、内核路径等本机项不变）。")).FontSize(12)
			ui.Text(c, i18n.T("· 用本机配置覆盖文件：原文件先备份到数据目录的 sync-backups 文件夹，再写入本机配置。")).FontSize(12)
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					ch.open = false
				}
				if ui.Button(c, i18n.T("用本机配置覆盖文件")).Clicked() {
					ch.open = false
					p.setSync(ch.mode, ch.dir, model.SyncUseLocal)
				}
				if ui.PrimaryButton(c, i18n.T("使用文件中的配置")).Clicked() {
					ch.open = false
					p.setSync(ch.mode, ch.dir, model.SyncUseFile)
				}
			})
		})
	})
}
