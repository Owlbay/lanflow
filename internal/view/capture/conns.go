package capture

import (
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// flowConn marks a row that is a connection, not a decrypted record.
const flowConn = "conn"

// connRows turns the core's connections (active and recently closed) into
// rows, leaving out the legs of decryption: their requests are listed as
// records. Each connection keeps a negative row ID for as long as it is
// listed.
func (p *Page) connRows() []model.CaptureFlow {
	api := p.e.API
	cs := append(api.Connections(), api.ClosedConnections()...)
	ids := make(map[string]int64, len(cs))
	p.conns = make(map[int64]model.Connection, len(cs))
	out := make([]model.CaptureFlow, 0, len(cs))
	for _, cn := range cs {
		if cn.CaptureLeg {
			continue
		}
		if _, dup := ids[cn.ID]; dup {
			continue
		}
		id, ok := p.connIDs[cn.ID]
		if !ok {
			p.nextConn--
			id = p.nextConn
		}
		ids[cn.ID] = id
		p.conns[id] = cn
		out = append(out, connFlow(id, cn))
	}
	p.connIDs = ids
	return out
}

// connFlow is the row for a connection: what the core knows without
// decrypting it.
func connFlow(id int64, cn model.Connection) model.CaptureFlow {
	host, port := splitHost(cn.Host)
	f := model.CaptureFlow{
		ID: id, Start: cn.Start, Process: cn.Process, ProcessPath: cn.ProcessPath,
		Proto: cn.Network, Host: host, Port: port,
		ReqSize: cn.Upload, RespSize: cn.Download,
		Done: !cn.ClosedAt.IsZero(), Flags: []string{flowConn},
	}
	switch port {
	case 80:
		f.Scheme = "http"
	case 443, 8443:
		f.Scheme = "https"
	}
	if f.Done {
		f.DurationMs = cn.ClosedAt.Sub(cn.Start).Milliseconds()
	}
	return f
}

func splitHost(hp string) (string, int) {
	h, ps, err := net.SplitHostPort(hp)
	if err != nil {
		return hp, 0
	}
	n, _ := strconv.Atoi(ps)
	return h, n
}

// selectedHost reports whether host is in the decryption selection
// (exact name, *.x for subdomains, +.x for x and its subdomains).
func selectedHost(set model.CaptureSettings, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range set.Domains {
		d = strings.ToLower(d)
		switch {
		case strings.HasPrefix(d, "+."):
			if host == d[2:] || strings.HasSuffix(host, d[1:]) {
				return true
			}
		case strings.HasPrefix(d, "*."):
			if strings.HasSuffix(host, d[1:]) {
				return true
			}
		case host == d:
			return true
		}
	}
	return false
}

// selectedApp reports whether the program (path, name) is in the
// selection: a "path" entry is a wildcard such as /Applications/Foo.app/*,
// a "name" entry a process name.
func selectedApp(set model.CaptureSettings, path, name string) bool {
	for _, a := range set.Apps {
		switch a.Kind {
		case "name":
			if name != "" && strings.EqualFold(a.Match, name) {
				return true
			}
		default:
			if path == "" || a.Match == "" {
				continue
			}
			if pre, wild := strings.CutSuffix(a.Match, "*"); wild && strings.HasPrefix(path, pre) || path == a.Match {
				return true
			}
		}
	}
	return false
}

// canDecryptHost: a TCP connection to a name (the root certificate never
// signs for an IP address).
func canDecryptHost(f model.CaptureFlow) bool {
	return f.Proto == "tcp" && f.Host != "" && net.ParseIP(f.Host) == nil
}

// decryptItems are the row menu's 解密此主机 / 解密此程序.
func (p *Page) decryptItems(m *ui.Menu, f model.CaptureFlow, st model.CaptureStatus) {
	hostSel := selectedHost(st.Settings, f.Host)
	if m.Item(i18n.T("解密此主机")).Checked(hostSel).Disabled(hostSel || !canDecryptHost(f)).Chosen() {
		p.handleArg("add-host:"+f.Host, st)
	}
	appSel := selectedApp(st.Settings, f.ProcessPath, f.Process)
	if m.Item(i18n.T("解密此程序")).Checked(appSel).Disabled(appSel || f.Process == "").Chosen() {
		p.handleArg("add-app:"+f.ProcessPath+"\n"+f.Process, st)
	}
}

// afterAdd opens the first-run explanation when it has not been accepted
// yet, so the choice just made can be turned on.
func (p *Page) afterAdd(st model.CaptureStatus) {
	if !st.Settings.Acknowledged {
		p.editing = true
	}
}

func (p *Page) closeButton(c *ui.Context) {
	ui.Button(c, "").Label(i18n.T("关闭详情")).Tooltip(i18n.T("关闭")).Padding(0, 4).Children(func() {
		ui.Text(c, "✕").FontSize(11)
	}).OnClick(func() { p.sel = 0 })
}

// connDetail is the panel for an undecrypted connection: what the core
// saw, and the way to decrypt it.
func (p *Page) connDetail(c *ui.Context, t *ui.Theme, f model.CaptureFlow, st model.CaptureStatus) {
	cn := p.conns[f.ID]
	ui.Column(c.Key("cdetail")).Width(detailWidth).FillHeight().BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Padding(12, 14, 6, 14).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Text(c, f.Host).Bold().FontSize(12).Selectable()
				state := i18n.T("进行中")
				if f.Done {
					state = i18n.T("已结束")
				}
				ui.Text(c, strings.ToUpper(f.Proto)+" · "+strconv.Itoa(f.Port)+" · "+state).FontSize(11).TextColor(t.TextMuted)
			})
			p.closeButton(c)
		})
		ui.Scroll(c).Grow(1).Padding(6, 14, 14, 14).Gap(8).Selectable().Children(func() {
			p.decryptCard(c, t, f, st)
			kv(c, t, i18n.T("程序"), env.Dash(cn.Process))
			if cn.ProcessPath != "" {
				kv(c, t, i18n.T("路径"), cn.ProcessPath)
			}
			kv(c, t, i18n.T("目标"), cn.Host)
			if cn.DstIP != "" && cn.DstIP != f.Host {
				kv(c, t, i18n.T("目标 IP"), cn.DstIP)
			}
			kv(c, t, i18n.T("入站"), env.Dash(cn.Type))
			rule := cn.Rule
			if cn.RulePayload != "" {
				rule += " " + cn.RulePayload
			}
			kv(c, t, i18n.T("规则"), env.Dash(rule))
			kv(c, t, i18n.T("链路"), env.Dash(strings.Join(cn.Chains, " → ")))
			kv(c, t, i18n.T("上传"), env.FmtBytes(cn.Upload))
			kv(c, t, i18n.T("下载"), env.FmtBytes(cn.Download))
			kv(c, t, i18n.T("开始"), cn.Start.Format("15:04:05"))
			if f.Done {
				kv(c, t, i18n.T("时长"), env.FmtDuration(time.Duration(f.DurationMs)*time.Millisecond))
			}
		})
	})
}

// decryptCard explains why only metadata is shown and offers decryption.
func (p *Page) decryptCard(c *ui.Context, t *ui.Theme, f model.CaptureFlow, st model.CaptureStatus) {
	hostSel := selectedHost(st.Settings, f.Host)
	appSel := selectedApp(st.Settings, f.ProcessPath, f.Process)
	var msg string
	switch {
	case (hostSel || appSel) && st.Enabled && !st.Paused:
		msg = i18n.T("已在解密选择中：这条连接建立于解密生效之前，之后的新请求会显示完整内容。")
	case hostSel || appSel:
		msg = i18n.T("已在解密选择中，开启解密后新的请求会显示完整内容。")
	case f.Proto != "tcp":
		msg = i18n.T("这是一条 UDP 连接，只能看到目标和流量。")
	default:
		msg = i18n.T("未解密：只能看到主机名、程序和流量。解密这个主机或程序后，之后的请求会显示方法、路径、头和正文。")
	}
	ui.Column(c).Padding(10).Gap(8).Radius(8).Background(t.SurfaceHover).Margin(0, 0, 4, 0).Children(func() {
		ui.Text(c, msg).FontSize(12)
		if hostSel && appSel {
			return
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if !hostSel {
				ui.PrimaryButton(c, i18n.T("解密此主机")).Disabled(!canDecryptHost(f)).OnClick(func() {
					p.handleArg("add-host:"+f.Host, st)
				})
			}
			if !appSel {
				ui.Button(c, i18n.T("解密此程序")).Disabled(f.Process == "").OnClick(func() {
					p.handleArg("add-app:"+f.ProcessPath+"\n"+f.Process, st)
				})
			}
		})
	})
}
