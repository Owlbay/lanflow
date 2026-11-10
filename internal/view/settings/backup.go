package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// 备份与同步 (Settings.WebDAV), in 设置 › 常规: two rows. 「WebDAV 网盘」
// summarises the server and opens the 配置… dialog (server, account,
// password — kept in the system keychain, or obfuscated in the settings
// directory where there is none — remote directory, how many backups to
// keep, automatic backup, 测试连接). 「备份」 shows the last backup, backs up
// now and opens the 管理备份… dialog, which lists the remote backups with
// 恢复 and 删除, each confirmed first. Remote calls run off the UI thread.

func (p *Page) backupSection(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	f := &p.f2
	info := e.API.WebDAVInfo()
	busy := f.davBusy != "" || info.Busy
	w := s.WebDAV
	e.Kit.Section(c, i18n.T("备份与同步"), i18n.T("把设置、覆写、自定义规则和订阅链接备份到 WebDAV 网盘"), func() {
		server := i18n.T("未配置")
		if strings.TrimSpace(w.URL) != "" {
			server = strings.TrimSpace(w.URL)
			if w.User != "" {
				server = w.User + " @ " + server
			}
		}
		e.Kit.Row(c, i18n.T("WebDAV 网盘"), server, func() {
			ui.Button(c, i18n.T("配置…")).OnClick(func() { f.davOpen, f.davErr = true, "" })
		})
		last := i18n.T("还没有备份过")
		if !info.LastBackupAt.IsZero() {
			last = i18n.Tf("上次备份：%s", env.FmtDateTime(info.LastBackupAt.Local()))
		}
		if info.LastError != "" {
			last = i18n.Tf("上次自动备份失败：%s", info.LastError)
		}
		e.Kit.Row(c, i18n.T("备份"), last, func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				ui.Button(c, i18n.T("管理备份…")).Disabled(busy).OnClick(func() {
					f.davListOpen = true
					if !f.davListed {
						p.davJob("list", p.reloadBackups, "")
					}
				})
				ui.PrimaryButton(c, i18n.T("立即备份")).Disabled(busy).OnClick(func() {
					p.davJob("backup", func() error {
						if err := e.API.BackupNow(); err != nil {
							return err
						}
						return p.reloadBackups()
					}, i18n.T("已备份到 WebDAV"))
				})
			})
		})
		p.backupConfigDialog(c, t, s, info, busy)
		p.backupListDialog(c, t, busy)
	})
}

// backupConfigDialog is the 配置… sheet; fields save as they commit.
func (p *Page) backupConfigDialog(c *ui.Context, t *ui.Theme, s model.Settings, info model.WebDAVInfo, busy bool) {
	e := p.e
	f := &p.f2
	if !f.davOpen {
		return
	}
	w := s.WebDAV
	ui.Modal(c, &f.davOpen, func() {
		ui.Column(c.Key("dav-config")).Width(520).Gap(12).Children(func() {
			ui.Text(c, i18n.T("WebDAV 网盘")).FontSize(15).Bold()
			ui.Text(c, i18n.T("填写网盘的 WebDAV 地址和账号；每一项在离开输入框时保存。")).FontSize(12).TextColor(t.TextMuted)
			ui.Column(c).Radius(8).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
				p.davField(c, i18n.T("服务器地址"), i18n.T("WebDAV 地址，例如 https://dav.jianguoyun.com/dav/"), "dav-url", &f.davURL, w.URL, 280, func(v string) {
					if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
						f.davErr = i18n.T("WebDAV 地址须是以 http:// 或 https:// 开头的完整地址")
						return
					}
					s.WebDAV.URL = v
					p.save(s, "", nil)
				})
				p.davField(c, i18n.T("用户名"), "", "dav-user", &f.davUser, w.User, 200, func(v string) { s.WebDAV.User = v; p.save(s, "", nil) })
				pwDesc := i18n.T("保存在系统钥匙串中")
				if info.PasswordStore == model.PasswordStoreFile {
					pwDesc = i18n.T("本机没有可用的钥匙串：密码经混淆后保存在设置目录（仅当前用户可读），并非加密")
				}
				if !info.HasPassword {
					pwDesc = i18n.Tf("未设置；%s", pwDesc)
				}
				e.Kit.Row(c, i18n.T("密码"), pwDesc, func() {
					ui.Row(c.Key("dav-pass-" + strconv.Itoa(f.davPassSession))).Gap(8).AlignItems(ui.Center).Children(func() {
						ph := i18n.T("输入新密码")
						if info.HasPassword {
							ph = "••••••••"
						}
						in := ui.TextInput(c.Key("dav-pass"), &f.davPass).Width(160).Password().Placeholder(ph).Label(i18n.T("密码"))
						save := func() {
							pw := f.davPass
							if pw == "" {
								return
							}
							f.davPass = ""
							f.davPassSession++
							e.Run(func() error { return e.API.SetWebDAVPassword(pw) }, i18n.T("密码已保存"))
						}
						if in.Submitted() {
							save()
						}
						ui.Button(c, i18n.T("保存")).Disabled(f.davPass == "").OnClick(save)
						if info.HasPassword {
							ui.Button(c, i18n.T("清除")).OnClick(func() {
								e.Run(func() error { return e.API.SetWebDAVPassword("") }, i18n.T("密码已清除"))
							})
						}
					})
				})
				p.davField(c, i18n.T("远程目录"), i18n.Tf("留空为 %s", model.WebDAVDefaultDir), "dav-dir", &f.davDir, w.Dir, 200, func(v string) {
					s.WebDAV.Dir = strings.Trim(v, "/")
					p.save(s, "", nil)
				})
				keepOpts := []string{i18n.T("全部保留"), "3", "5", "10", "20"}
				keepVals := []int{0, 3, 5, 10, 20}
				keepCur := keepOpts[0]
				for i, v := range keepVals {
					if v == w.Keep {
						keepCur = keepOpts[i]
					}
				}
				p.selectRow(c, i18n.T("保留份数"), i18n.T("备份成功后删除更早的备份"), keepCur, keepOpts, func(v string) {
					for i, o := range keepOpts {
						if o == v {
							s.WebDAV.Keep = keepVals[i]
						}
					}
					p.save(s, "", nil)
				})
				autoOpts := []string{i18n.T("关闭|开关"), i18n.T("每天"), i18n.T("每周")}
				autoVals := []string{"", model.WebDAVDaily, model.WebDAVWeekly}
				p.selectRow(c, i18n.T("自动备份"), i18n.T("澜流运行时按间隔自动备份"), mapName(w.AutoBackup, autoVals, autoOpts), autoOpts, func(v string) {
					s.WebDAV.AutoBackup = mapValue(v, autoOpts, autoVals)
					p.save(s, "", nil)
				})
			})
			if f.davErr != "" {
				ui.Text(c, f.davErr).FontSize(11).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				ui.Button(c, i18n.T("测试连接")).Disabled(busy).OnClick(func() {
					p.davJob("test", func() error { return e.API.TestWebDAV() }, i18n.T("连接成功，远程目录可用"))
				})
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("完成")).Clicked() {
					f.davOpen = false
				}
			})
		})
	})
}

// backupListDialog is the 管理备份… sheet.
func (p *Page) backupListDialog(c *ui.Context, t *ui.Theme, busy bool) {
	f := &p.f2
	if !f.davListOpen {
		return
	}
	ui.Modal(c, &f.davListOpen, func() {
		ui.Column(c.Key("dav-list")).Width(520).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("服务器上的备份")).FontSize(15).Bold()
				if f.davListed {
					ui.Text(c, i18n.Tf("%d 份备份", len(f.davBackups))).FontSize(12).TextColor(t.TextMuted)
				}
				ui.Spacer(c)
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				ui.Button(c, i18n.T("刷新")).Disabled(busy).OnClick(func() { p.davJob("list", p.reloadBackups, "") })
			})
			if f.davErr != "" {
				ui.Text(c, f.davErr).FontSize(11).TextColor(t.Danger)
			}
			switch {
			case !f.davListed:
				ui.Text(c, i18n.T("正在读取服务器上的备份…")).FontSize(12).TextColor(t.TextMuted)
			case len(f.davBackups) == 0:
				ui.Text(c, i18n.T("服务器上还没有备份")).FontSize(12).TextColor(t.TextMuted)
			default:
				ui.Scroll(c.Key("dav-rows")).MaxHeight(320).Children(func() {
					ui.Column(c).Radius(8).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
						for _, b := range f.davBackups {
							p.backupRow(c, b, busy)
						}
					})
				})
			}
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, i18n.T("关闭")).Clicked() {
					f.davListOpen = false
				}
			})
		})
	})
}

// backupRow is one remote backup with 恢复 and 删除.
func (p *Page) backupRow(c *ui.Context, b model.WebDAVBackup, busy bool) {
	e := p.e
	when := b.Modified.Local().Format("2006-01-02 15:04:05")
	desc := when
	if b.Size > 0 {
		desc += " · " + env.FmtBytes(b.Size)
	}
	e.Kit.Row(c, b.Name, desc, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Button(c, i18n.T("恢复")).Disabled(busy).OnClick(func() {
				e.Confirm(i18n.Tf("恢复 %s 的备份？", when), i18n.T("备份中的设置、覆写、自定义规则和订阅会应用到本机；内核文件路径和 TUN 开关保持不变。"), i18n.T("恢复"), false, func() {
					p.davJob("restore", func() error {
						summary, err := e.API.RestoreBackup(b.Name)
						if err != nil {
							return err
						}
						p.mu.Lock()
						p.toast = summary
						p.mu.Unlock()
						return nil
					}, "")
				})
			})
			ui.Button(c, i18n.T("删除")).Disabled(busy).OnClick(func() {
				e.Confirm(i18n.Tf("删除 %s 的备份？", when), i18n.T("备份会从服务器上删除，此操作无法撤销。"), i18n.T("删除"), true, func() {
					p.davJob("delete", func() error {
						if err := e.API.DeleteBackup(b.Name); err != nil {
							return err
						}
						return p.reloadBackups()
					}, i18n.T("备份已删除"))
				})
			})
		})
	})
}

// davField draws one committed text field of the WebDAV form.
func (p *Page) davField(c *ui.Context, label, desc, key string, f *field, current string, width float32, set func(string)) {
	p.e.Kit.Row(c, label, desc, func() {
		if p.f2.davErr == "" || key != "dav-url" {
			f.show(current)
		}
		in := ui.TextInput(c.Key(key), &f.text).Width(width).Label(label).Invalid(key == "dav-url" && p.f2.davErr != "")
		if in.Changed() && key == "dav-url" {
			p.f2.davErr = ""
		}
		if f.commit(in) {
			v := strings.TrimSpace(f.text)
			if v != current {
				set(v)
			}
		}
	})
}

// davJob runs one remote operation, marking the section busy meanwhile;
// its error shows in the open dialog and as a toast.
func (p *Page) davJob(kind string, job func() error, ok string) {
	p.f2.davBusy = kind
	p.f2.davErr = ""
	p.e.Run(func() error {
		err := job()
		p.mu.Lock()
		p.f2.davBusy = ""
		if err != nil {
			p.f2.davErr = err.Error()
		}
		p.mu.Unlock()
		return err
	}, ok)
}

// reloadBackups fetches the remote list (off the UI thread).
func (p *Page) reloadBackups() error {
	list, err := p.e.API.ListBackups()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.f2.davBackups, p.f2.davListed = list, true
	p.mu.Unlock()
	return nil
}
