package proxies

import (
	"bytes"
	"image"
	_ "image/jpeg" // group icons may be JPEG
	_ "image/png"  // or PNG
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// Group icons (docs/design.md §5.2, the proxy-group `icon` field) and
// 「定位当前节点」: scroll to the group's selected member and flash it.

// iconSize is the icon's edge in the group header, in DIPs.
const iconSize = 16

// highlightFor is how long the located node stays tinted.
const highlightFor = 1500 * time.Millisecond

// locateState is the node being revealed: set by the header button,
// consumed by the node row of that group's current member.
type locateState struct {
	group string
	until time.Time // highlight end; zero = nothing to show
	shown bool      // the row scrolled into view once
}

// groupIcon draws a group's icon at 16 pt, if the reference decodes to an
// image the UI can draw (SVG, PNG, JPEG); nothing otherwise. Decoded
// images are cached by reference.
func (p *Page) groupIcon(c *ui.Context, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" || p.iconFail[ref] {
		return
	}
	src, ok := p.icons[ref]
	if !ok {
		data, have := p.e.API.IconData(ref)
		if !have {
			return // not loaded yet (TopicProxies redraws when it lands), or never
		}
		src = decodeIcon(data)
		if src == nil {
			p.iconFail[ref] = true
			return
		}
		p.icons[ref] = src
	}
	ui.Image(c, src).Size(iconSize, iconSize).Shrink(0).Radius(3)
}

// decodeIcon turns image bytes into something ui.Image draws: an SVG, or
// a bitmap for the raster formats the standard library decodes.
func decodeIcon(data []byte) ui.ImageSource {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.HasPrefix(trimmed, []byte("<?xml")) {
		if s, err := ui.ParseSVG(trimmed); err == nil {
			return s
		}
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return ui.NewBitmap(img)
}

// locateNow asks the page to reveal group's current member: the group is
// expanded, its row scrolls into view and stays tinted for a moment.
func (p *Page) locateNow(c *ui.Context, group string) {
	p.open[group] = true
	p.openInit[group] = true
	p.locate = locateState{group: group, until: c.Now().Add(highlightFor)}
	p.persist()
}

// locating reports whether the member row of group's current node should
// be revealed and tinted now; it schedules the frame that ends the tint.
func (p *Page) locating(c *ui.Context, group string, cur bool) (reveal, tint bool) {
	l := &p.locate
	if !cur || l.group != group || l.until.IsZero() {
		return false, false
	}
	if !c.Now().Before(l.until) {
		*l = locateState{}
		return false, false
	}
	c.After(l.until.Sub(c.Now()))
	reveal = !l.shown
	l.shown = true
	return reveal, true
}
