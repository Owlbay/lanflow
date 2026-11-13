package settings

import (
	"bytes"
	"image"
	_ "image/png"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/trayart"
)

// menuBarPreview caches the rendered icon of the 菜单栏 preview.
type menuBarPreview struct {
	mu  sync.Mutex
	key string
	src ui.ImageSource
	w   float32
}

// menuBarSection is 菜单栏 (macOS, docs/design.md §7): what shows beside
// the icon, with a preview drawn like the real thing.
func (p *Page) menuBarSection(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	e.Kit.Section(c, i18n.T("菜单栏显示"), i18n.T("图标旁显示的内容；全部关闭时只显示图标"), func() {
		p.menuBarPreview(c, t, s)
		speedOpts := []string{i18n.T("上下行"), i18n.T("仅下行"), i18n.T("不显示")}
		speedVals := []string{model.TraySpeedBoth, model.TraySpeedDown, model.TraySpeedOff}
		p.selectRow(c, i18n.T("实时速度"), "", mapName(s.TraySpeed, speedVals, speedOpts), speedOpts, func(v string) {
			s.TraySpeed = mapValue(v, speedOpts, speedVals)
			s.HideTraySpeed = s.TraySpeed == model.TraySpeedOff
			p.save(s, "", nil)
		})
		p.switchRow(c, i18n.T("流量图"), i18n.T("图标旁显示最近 8 秒的流量"), s.TrayGraph, func(on bool) { s.TrayGraph = on; p.save(s, "", nil) })
		p.switchRow(c, i18n.T("当前节点"), i18n.T("主策略组正在使用的节点"), s.TrayNode, func(on bool) { s.TrayNode = on; p.save(s, "", nil) })
		p.switchRow(c, i18n.T("代理模式"), i18n.T("处于全局或直连模式时显示"), s.TrayMode, func(on bool) { s.TrayMode = on; p.save(s, "", nil) })
	})
}

// menuBarPreview draws the icon and the text as the menu bar would, from
// the current state (sample traffic when the core is stopped).
func (p *Page) menuBarPreview(c *ui.Context, t *ui.Theme, s model.Settings) {
	api := p.e.API
	st := api.Status()
	if st.Core != model.CoreRunning {
		st = model.Status{Core: model.CoreRunning, SystemProxy: true, Mode: model.ModeGlobal, Up: 24 << 10, Down: 1300 << 10}
	}
	if st.Mode == model.ModeRule && s.TrayMode {
		st.Mode = model.ModeGlobal // show what the option adds
	}
	node := ""
	if s.TrayNode {
		node = i18n.T("香港 01")
		px := api.Proxies()
		for _, g := range px.Groups {
			if g.Name == px.MainGroup && g.Now != "" {
				node = g.Now
			}
		}
	}
	look := trayart.Look{State: trayart.StateOf(st), Capture: api.CaptureStatus().Enabled}
	if s.TrayGraph {
		up, down := api.TrafficHistory()
		look.Graph = trayart.Graph(up, down)
		idle := true
		for _, v := range look.Graph {
			idle = idle && v == 0
		}
		if idle { // a sample shape, so the option shows
			look.Graph = []float64{0.2, 0.35, 0.7, 0.9, 0.5, 0.3, 1, 0.6}
		}
	}
	title := trayart.Title(st, node, s)
	src, w := p.menuBar.icon(look, t.Dark)

	bar := ui.RGB(236, 236, 236)
	ink := ui.RGB(0, 0, 0)
	if t.Dark {
		bar, ink = ui.RGB(40, 40, 42), ui.RGB(255, 255, 255)
	}
	ui.Row(c).Padding(10, 14).Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Padding(3, 10).Radius(6).Background(bar).Children(func() {
			if src != nil {
				ui.Image(c, src).Width(w).Height(18)
			}
			if title != "" {
				ui.Text(c, title).FontSize(13).TextColor(ink).FontFeatures("tnum").SingleLine()
			}
		})
	})
}

func (m *menuBarPreview) icon(l trayart.Look, dark bool) (ui.ImageSource, float32) {
	key := l.Key()
	if dark {
		key += "|dark"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if key == m.key {
		return m.src, m.w
	}
	img, _, err := image.Decode(bytes.NewReader(trayart.PNG(l, trayart.Ink{Dark: dark})))
	if err != nil {
		return nil, 0
	}
	m.key, m.src, m.w = key, ui.NewBitmap(img), float32(img.Bounds().Dx())/2
	return m.src, m.w
}
