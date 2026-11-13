package capture

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/capture/redact"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// 重新发送 (docs/capture.md §7.5): a recorded request opens in an editor
// (method, URL, headers, body), is sent again through the core and the
// answer shows below, like an API testing tool.

var replayMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

type replayState struct {
	mu sync.Mutex

	open    bool
	session int // a new editor gets fresh widget state
	loading bool

	method  string
	url     string
	headers string // "Name: value" per line
	body    string
	binary  []byte // a body that is not text: sent unchanged, not editable
	note    string // e.g. the recorded body was cut
	direct  bool
	follow  bool
	tab     int // 0 headers, 1 body

	busy   bool
	res    *model.ReplayResult
	err    string
	resTab int // 0 body, 1 headers
}

// openReplay loads record id and opens the editor with it; send sends it
// at once (重新发送 without editing).
func (p *Page) openReplay(id int64, send bool) {
	r := &p.rp
	r.mu.Lock()
	r.open, r.loading, r.res, r.err, r.busy = true, true, nil, "", false
	r.session++
	r.mu.Unlock()
	api, redraw := p.e.API, p.e.Redraw
	go func() {
		d, err := api.CaptureFlow(id)
		r.mu.Lock()
		r.loading = false
		if err != nil {
			r.err = err.Error()
		} else {
			r.fill(d)
		}
		r.mu.Unlock()
		redraw()
		if send && err == nil {
			p.sendReplay()
		}
	}()
}

// fill takes the editor's content from a record. Call with mu held.
func (r *replayState) fill(d model.CaptureFlowDetail) {
	f := d.Flow
	r.method = strings.ToUpper(f.Method)
	if r.method == "" {
		r.method = "GET"
	}
	r.url = flowURL(f, false)
	r.binary, r.body, r.note, r.tab = nil, "", "", 0
	hdr := d.ReqHeader
	body := d.ReqBody
	if enc := header(hdr, "content-encoding"); enc != "" {
		// a compressed request body is edited as text and sent plain
		if dec, ok := redact.Decode(body, enc); ok {
			body = dec
			hdr = dropHeader(hdr, "content-encoding")
		}
	}
	var lines []string
	for _, kv := range hdr {
		if strings.HasPrefix(kv.Key, ":") {
			continue // HTTP/2 pseudo headers: the URL and method carry them
		}
		lines = append(lines, kv.Key+": "+kv.Value)
	}
	r.headers = strings.Join(lines, "\n")
	switch {
	case len(body) == 0:
	case utf8.Valid(body):
		r.body = string(body)
	default:
		r.binary = body
	}
	if d.ReqBodySize > int64(len(d.ReqBody)) {
		r.note = i18n.Tf("记录的请求体被截断（%s / %s），发送的是截断后的内容。", env.FmtBytes(int64(len(d.ReqBody))), env.FmtBytes(d.ReqBodySize))
	}
	if len(body) > 0 {
		r.tab = 1
	}
}

func dropHeader(h []model.KV, name string) []model.KV {
	var out []model.KV
	for _, kv := range h {
		if !strings.EqualFold(kv.Key, name) {
			out = append(out, kv)
		}
	}
	return out
}

// request is what the editor holds. Call with mu held.
func (r *replayState) request() model.ReplayRequest {
	req := model.ReplayRequest{Method: strings.TrimSpace(r.method), URL: strings.TrimSpace(r.url), Direct: r.direct, FollowRedirects: r.follow}
	req.Header = parseHeaderLines(r.headers)
	if r.binary != nil {
		req.Body = r.binary
	} else if r.body != "" {
		req.Body = []byte(r.body)
	}
	return req
}

func parseHeaderLines(s string) []model.KV {
	var out []model.KV
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		k, v, _ := strings.Cut(l, ":")
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, model.KV{Key: k, Value: strings.TrimSpace(v)})
		}
	}
	return out
}

func (p *Page) sendReplay() {
	r := &p.rp
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return
	}
	req := r.request()
	r.busy, r.err = true, ""
	r.mu.Unlock()
	api, redraw := p.e.API, p.e.Redraw
	redraw()
	go func() {
		res, err := api.ReplayRequest(req)
		r.mu.Lock()
		r.busy = false
		if err != nil {
			r.err, r.res = err.Error(), nil
		} else {
			r.res = &res
		}
		r.mu.Unlock()
		redraw()
	}()
}

// curl renders the editor as a curl command (single-quoted for a POSIX
// shell). Call with mu held.
func (r *replayState) curl() string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	req := r.request()
	parts := []string{"curl"}
	if req.Method != "" && req.Method != "GET" {
		parts = append(parts, "-X", q(req.Method))
	}
	parts = append(parts, q(req.URL))
	for _, kv := range req.Header {
		parts = append(parts, "-H", q(kv.Key+": "+kv.Value))
	}
	if len(req.Body) > 0 {
		if r.binary != nil {
			parts = append(parts, "--data-binary", "@body.bin")
		} else {
			parts = append(parts, "--data-raw", q(string(req.Body)))
		}
	}
	if req.FollowRedirects {
		parts = append(parts, "-L")
	}
	return strings.Join(parts, " ")
}

func (p *Page) replayModal(c *ui.Context, t *ui.Theme) {
	r := &p.rp
	r.mu.Lock()
	open := r.open
	r.mu.Unlock()
	if !open {
		return
	}
	e := p.e
	ui.Modal(c, &r.open, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		ui.Column(c.Key(fmt.Sprint("replay-", r.session))).Width(780).Gap(10).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, i18n.T("重新发送请求")).FontSize(15).FontWeight(600).Grow(1)
			})
			if r.loading {
				ui.Spinner(c).Size(14, 14).Label(i18n.T("加载中"))
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Select(c.Key("rp-method"), &r.method, replayMethods).Label(i18n.T("方法")).Width(110)
				ui.TextInput(c.Key("rp-url"), &r.url).Font("monospace").FontSize(12).Label(i18n.T("网址")).Grow(1)
				if ui.PrimaryButton(c, i18n.T("发送")).Disabled(r.busy || strings.TrimSpace(r.url) == "").Clicked() {
					go p.sendReplay()
				}
			})
			ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
				ui.Segmented(c.Key("rp-tab"), &r.tab, i18n.T("请求头"), i18n.T("请求体")).Label(i18n.T("请求页签"))
				ui.Spacer(c)
				ui.Checkbox(c.Key("rp-direct"), &r.direct, i18n.T("直接连接（不经内核）"))
				ui.Checkbox(c.Key("rp-follow"), &r.follow, i18n.T("跟随重定向"))
			})
			switch r.tab {
			case 0:
				ui.TextArea(c.Key("rp-headers"), &r.headers).Font("monospace").FontSize(12).Lines(6, 12).
					Placeholder("Content-Type: application/json").Label(i18n.T("请求头"))
				ui.Text(c, i18n.T("每行一个「名称: 值」；Host 会作为请求的主机名，Content-Length 等由发送时自动计算。")).FontSize(11).TextColor(t.TextMuted)
			case 1:
				if r.binary != nil {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, i18n.Tf("二进制请求体（%s），按原样发送，不能编辑。", env.FmtBytes(int64(len(r.binary))))).FontSize(12).TextColor(t.TextMuted).Grow(1)
						if ui.Button(c, i18n.T("清空")).Clicked() {
							r.binary = nil
						}
					})
				} else {
					ui.TextArea(c.Key("rp-body"), &r.body).Font("monospace").FontSize(12).Lines(6, 12).Label(i18n.T("请求体"))
				}
			}
			if r.note != "" {
				ui.Text(c, r.note).FontSize(11).TextColor(t.Warning)
			}
			if r.err != "" {
				ui.Text(c, r.err).FontSize(12).TextColor(t.Danger).Selectable()
			}
			p.replayResult(c, t)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, i18n.T("复制为 cURL")).Clicked() {
					c.WriteClipboard(r.curl())
					e.Toast(i18n.T("已复制（包含未打码的凭据）"), false)
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("关闭")).Clicked() {
					r.open = false
				}
			})
		})
	})
}

// replayResult shows the answer. Call with mu held.
func (p *Page) replayResult(c *ui.Context, t *ui.Theme) {
	r := &p.rp
	switch {
	case r.busy:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(14, 14).Label(i18n.T("发送中"))
			ui.Text(c, i18n.T("发送中…")).FontSize(12).TextColor(t.TextMuted)
		})
		return
	case r.res == nil:
		return
	}
	res := r.res
	ui.Column(c).Gap(6).Padding(10).Radius(6).Background(t.Surface).Border(1, t.Border).Children(func() {
		if res.Error != "" && res.Status == 0 {
			ui.Text(c, i18n.Tf("请求失败（%d ms）：%s", res.DurationMs, res.Error)).FontSize(12).TextColor(t.Danger).Selectable()
			return
		}
		via := i18n.T("经内核")
		if res.Via == "direct" {
			via = i18n.T("直接连接")
		}
		col := t.Text
		if res.Status >= 400 {
			col = t.Danger
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, strconv.Itoa(res.Status)+" "+res.StatusText).Bold().FontSize(13).TextColor(col)
			ui.Text(c, fmt.Sprintf("%s · %s · %s · %s", res.Proto, fmtDur(res.DurationMs), env.FmtBytes(res.BodySize), via)).FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c)
			ui.Segmented(c.Key("rp-restab"), &r.resTab, i18n.T("响应体"), i18n.T("响应头")).Label(i18n.T("响应页签"))
		})
		if res.Truncated {
			ui.Text(c, i18n.Tf("响应体超过 %s，只显示前面部分。", env.FmtBytes(4<<20))).FontSize(11).TextColor(t.TextMuted)
		}
		ui.Scroll(c).MaxHeight(260).Selectable().Children(func() {
			if r.resTab == 1 {
				p.headers(c, t, res.Header)
				return
			}
			// already decoded by the service
			p.bodyView(c, t, -1<<40-int64(r.session), res.Body, res.BodySize, dropHeader(res.Header, "content-encoding"))
		})
	})
}
