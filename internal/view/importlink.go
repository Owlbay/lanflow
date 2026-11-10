// Importing from outside the window: lanflow:// and clash:// links, and
// YAML files dropped on it.

package view

import (
	"log"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
	"lanflow/internal/view/subs"
)

// OpenImportLink shows the window and opens the import-from-link dialog
// with rawURL prefilled; nothing is imported until the user confirms.
// It accepts the app's own deep links and the Clash convention:
//
//	lanflow://import?url=<subscription>&name=<name>
//	clash://install-config?url=<subscription>&name=<name>
//
// Anything else is ignored. It is safe from any goroutine.
func (s *Shell) OpenImportLink(rawURL string) {
	target, name, ok := ParseImportLink(rawURL)
	if !ok {
		log.Printf("lanflow: ignoring link %q", rawURL)
		return
	}
	s.post(func() { s.navigate(env.Subs, env.ImportURLArg(target, name)) })
	s.ShowWindow()
}

// ParseImportLink extracts the subscription URL (http/https) and optional
// name from a lanflow:// or clash:// link.
func ParseImportLink(rawURL string) (target, name string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "lanflow":
		if h := strings.ToLower(u.Host + u.Path); h != "import" && h != "import/" && h != "install-config" && h != "install-config/" {
			return "", "", false
		}
	case "clash", "clashmeta", "mihomo":
		if h := strings.ToLower(u.Host + u.Path); h != "install-config" && h != "install-config/" {
			return "", "", false
		}
	default:
		return "", "", false
	}
	q := u.Query()
	target = strings.TrimSpace(q.Get("url"))
	t, err := url.Parse(target)
	if err != nil || (t.Scheme != "http" && t.Scheme != "https") || t.Host == "" {
		return "", "", false
	}
	return target, strings.TrimSpace(q.Get("name")), true
}

// dropOverlay highlights the window while YAML files are dragged over it.
func (s *Shell) dropOverlay(c *ui.Context) {
	t := c.Theme()
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Left(12).Top(12).Right(12).Bottom(12).Center().Gap(6).Radius(12).
			Border(2, t.Accent).BorderStyle(ui.BorderDashed).Background(t.Background.Alpha(0.88)).PassThrough().Children(func() {
			ui.Box(c).TextColor(t.Accent).Children(func() { s.env.Kit.Icon(c, "down", 28) })
			ui.Text(c, i18n.T("松开以导入")).FontSize(15).FontWeight(600)
			ui.Text(c, i18n.T("支持 .yaml / .yml 文件")).FontSize(12).TextColor(t.TextMuted)
		})
	})
}

// importFiles imports dropped YAML files as subscriptions.
func (s *Shell) importFiles(paths []string) {
	var yaml []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml":
			yaml = append(yaml, p)
		}
	}
	if len(yaml) == 0 {
		s.toast(toastReq{msg: i18n.T("只能导入 .yaml / .yml 配置文件"), isError: true})
		return
	}
	// Without a subscription yet, the first dropped file becomes the
	// current one and starts the core, like the other import paths
	// (docs/design.md §8.1: Toast「已导入并启动」).
	none := !s.api.Status().HasSubscriptions
	s.navigate(env.Subs, "")
	for i, p := range yaml {
		path, first := p, none && i == 0
		s.run(func() error { return subs.ImportFile(s.api, path, first) }, subs.ImportToast(path, first))
	}
}
