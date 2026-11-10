package view

import "github.com/egoist/mygo/ui"

// svg wraps 16×16 line-icon shapes in an SVG that takes the text color.
func svg(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

// icons are the monochrome line icons by the names env.Kit.Icon takes.
var icons = map[string]*ui.SVG{
	"overview": svg(`<path d="M2.5 11.5a5.5 5.5 0 1 1 11 0"/><path d="M8 11.5l2.6-3.2"/><circle cx="8" cy="11.5" r=".6" fill="currentColor"/>`),
	"proxies":  svg(`<circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c1.8 1.8 2.6 3.8 2.6 6S9.8 12.2 8 14C6.2 12.2 5.4 10.2 5.4 8S6.2 3.8 8 2z"/>`),
	"subs":     svg(`<rect x="4.5" y="2.5" width="9" height="11" rx="1.5"/><path d="M2.5 5v8a1.5 1.5 0 0 0 1.5 1.5h7M7 6h4M7 8.5h4"/>`),
	"conns":    svg(`<path d="M5 13V3M2.5 5.5L5 3l2.5 2.5M11 3v10M8.5 10.5L11 13l2.5-2.5"/>`),
	"logs":     svg(`<path d="M2.5 3.5h11M2.5 6.5h11M2.5 9.5h7M2.5 12.5h9"/>`),
	"rules":    svg(`<path d="M2.5 4h1.5M2.5 8h1.5M2.5 12h1.5M6.5 4h7M6.5 8h7M6.5 12h4.5"/>`),
	"net":      svg(`<path d="M1.8 6.3a9 9 0 0 1 12.4 0M4 8.7a5.8 5.8 0 0 1 8 0M6.2 11.1a2.6 2.6 0 0 1 3.6 0"/><circle cx="8" cy="13.2" r=".7" fill="currentColor"/>`),
	"stats":    svg(`<path d="M2.5 13.5h11"/><path d="M4 13.5V8.5M7 13.5V4.5M10 13.5V7M13 13.5V2.5" stroke-width="1.8"/>`),
	"settings": svg(`<circle cx="8" cy="8" r="2.2"/><path d="M8 1.8v1.6M8 12.6v1.6M1.8 8h1.6M12.6 8h1.6M3.6 3.6l1.1 1.1M11.3 11.3l1.1 1.1M3.6 12.4l1.1-1.1M11.3 4.7l1.1-1.1"/>`),
	"search":   svg(`<circle cx="7" cy="7" r="4.2"/><path d="M10.2 10.2l3.3 3.3"/>`),
	"more":     svg(`<circle cx="3.5" cy="8" r="1" fill="currentColor"/><circle cx="8" cy="8" r="1" fill="currentColor"/><circle cx="12.5" cy="8" r="1" fill="currentColor"/>`),
	"plus":     svg(`<path d="M8 3v10M3 8h10"/>`),
	"refresh":  svg(`<path d="M13 8a5 5 0 1 1-1.5-3.6M13 2.5v3h-3"/>`),
	"bolt":     svg(`<path d="M9 1.8L3.8 9h4l-1 5.2L12.2 7h-4z"/>`),
	"pause":    svg(`<path d="M5.5 3.5v9M10.5 3.5v9"/>`),
	"play":     svg(`<path d="M5 3.2l7.5 4.8L5 12.8z"/>`),
	"copy":     svg(`<rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 3.5V3A1.5 1.5 0 0 0 9 1.5H4A1.5 1.5 0 0 0 2.5 3v5A1.5 1.5 0 0 0 4 9.5h.5"/>`),
	"close":    svg(`<path d="M4 4l8 8M12 4l-8 8"/>`),
	"down":     svg(`<path d="M8 3v10M4 9l4 4 4-4"/>`),
	"warn":     svg(`<path d="M8 2l6.5 11.5h-13z"/><path d="M8 6.5v3.2"/><circle cx="8" cy="11.6" r=".5" fill="currentColor"/>`),
	"check":    svg(`<path d="M3 8.5l3.2 3L13 4.5"/>`),
	"doc":      svg(`<rect x="3.5" y="1.5" width="9" height="13" rx="1.5"/><path d="M6 5h4M6 8h4M6 11h2"/>`),
	"power":    svg(`<path d="M8 1.8v6M4.4 4.2a5 5 0 1 0 7.2 0"/>`),
	"link":     svg(`<path d="M7 9a3 3 0 0 0 4.2.2l2-2a3 3 0 0 0-4.2-4.3l-1 1M9 7a3 3 0 0 0-4.2-.2l-2 2a3 3 0 0 0 4.2 4.3l1-1"/>`),
	"info":     svg(`<circle cx="8" cy="8" r="6"/><path d="M8 7.2v4"/><circle cx="8" cy="5" r=".6" fill="currentColor"/>`),
	"chevron":  svg(`<path d="M6 3.5L10.5 8 6 12.5"/>`),
	"probe":    svg(`<path d="M8 1.8l5 1.9v4.1c0 3.2-2.1 5.3-5 6.4-2.9-1.1-5-3.2-5-6.4V3.7z"/><path d="M5.8 8.2l1.6 1.6 3-3.2"/>`),
	"up":       svg(`<path d="M8 13V3M4 7l4-4 4 4"/>`),
	"capture":  svg(`<rect x="3.5" y="7" width="9" height="7" rx="1.5"/><path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2"/><path d="M8 9.6v1.8"/>`),
	"locate":   svg(`<circle cx="8" cy="8" r="4.5"/><circle cx="8" cy="8" r="1" fill="currentColor"/><path d="M8 1.5v3M8 11.5v3M1.5 8h3M11.5 8h3"/>`),
}

// pageIcons maps sidebar pages to their icon names.
var pageIcons = map[string]string{
	"overview": "overview", "proxies": "proxies", "subs": "subs", "conns": "conns",
	"logs": "logs", "rules": "rules", "net": "net", "stats": "stats", "probe": "probe", "capture": "capture", "settings": "settings",
}

// Icon returns the SVG of a named icon, nil if unknown.
func Icon(name string) *ui.SVG { return icons[name] }
