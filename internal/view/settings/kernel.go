package settings

import (
	"path/filepath"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// Core update (docs/design.md:519 「更新」) and settings export / import
// (docs/design.md:485). The download itself is API.DownloadCore; the page
// only decides when to ask first and shows the progress it publishes.

// updateCore downloads and installs version ("" = the pinned release).
// With the TUN helper installed, the helper has to be registered again
// for the new binary (it only runs the core recorded at install time), so
// the user is told about the administrator prompt first.
func (p *Page) updateCore(version string) {
	e := p.e
	ok := i18n.T("Mihomo 内核已下载完成")
	if version != "" {
		ok = i18n.Tf("内核已更新到 %s", version)
	}
	run := func() {
		e.Run(func() error { return e.API.DownloadCore(version) }, ok)
	}
	if !e.API.TUNHelperInstalled() {
		run()
		return
	}
	e.Confirm(i18n.T("更新内核"), i18n.T("已安装权限组件：更新后需要重新登记新内核，期间会请求一次管理员权限；内核会在更新完成后重新启动。"), i18n.T("更新"), false, run)
}

// exportDialog is the 导出 options sheet.
type exportDialog struct {
	open bool
	subs bool // include subscription URLs (they carry access tokens)
}

func settingsFileFilter() []mygo.FileFilter {
	return []mygo.FileFilter{{Name: i18n.T("澜流设置"), Extensions: []string{"json"}}}
}

func (p *Page) exportDialog(c *ui.Context, t *ui.Theme) {
	if !p.export.open {
		return
	}
	ui.Modal(c, &p.export.open, func() {
		ui.Column(c).Width(420).Gap(12).Children(func() {
			ui.Text(c, i18n.T("导出设置")).FontSize(15).Bold()
			ui.Text(c, i18n.T("导出常规、代理、DNS、嗅探与内核设置，以及覆写和自定义规则。内核文件路径不会导出。")).FontSize(12).TextColor(t.TextMuted)
			ui.Checkbox(c, &p.export.subs, i18n.T("包含订阅链接"))
			if p.export.subs {
				ui.Text(c, i18n.T("订阅链接含有你的访问令牌，导出的文件请勿分享给他人。")).FontSize(12).TextColor(t.Warning)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					p.export.open = false
				}
				if ui.PrimaryButton(c, i18n.T("选择位置并导出…")).Clicked() {
					include := p.export.subs
					p.export.open = false
					go p.exportSettings(include)
				}
			})
		})
	})
}

// exportSettings asks where to save and writes the file. The system
// dialog blocks, so this runs on its own goroutine.
func (p *Page) exportSettings(includeSubs bool) {
	e := p.e
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       i18n.T("导出澜流设置"),
		DefaultPath: "lanflow-settings.json",
		Filters:     settingsFileFilter(),
	})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".json"
	}
	e.Run(func() error { return e.API.ExportSettings(path, includeSubs) }, i18n.Tf("已导出到 %s", filepath.Base(path)))
}

// importSettings picks a file and applies it; the summary is toasted.
func (p *Page) importSettings() {
	e := p.e
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:   i18n.T("导入澜流设置"),
		Message: i18n.T("选择之前用「导出…」保存的 JSON 文件。导入会应用其中的设置、覆写和自定义规则。"),
		Filters: settingsFileFilter(),
	})
	if err != nil || len(paths) == 0 {
		return
	}
	e.Run(func() error {
		summary, err := e.API.ImportSettings(paths[0])
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.toast = summary
		p.mu.Unlock()
		return nil
	}, "")
}
