package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// GitHub download mirror (Settings.DownloadMirror): a URL prefix such as
// https://ghfast.top/ put in front of GitHub download links, for users
// who cannot reach GitHub directly. It applies when a link is used (the
// core download in coredl.go and the geo URLs handed to the core's
// config in lifecycle.go); the stored URLs stay as they are, so turning
// the mirror off needs no cleanup. Mirrors are third-party services.

// MirrorPresets are the mirrors offered in the settings, in display order.
var MirrorPresets = []string{
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
}

// mirrorHosts are the hosts a mirror prefix is applied to.
var mirrorHosts = map[string]bool{
	"github.com":                    true,
	"objects.githubusercontent.com": true,
	"raw.githubusercontent.com":     true,
	"codeload.github.com":           true,
}

// mirrorProbeURL is the GitHub link TestMirrors asks each mirror for
// (HEAD only); tests point it at a local server.
var mirrorProbeURL = geoBase + "geoip.metadb"

const mirrorTestTimeout = 8 * time.Second

// NormalizeMirror trims a mirror prefix and makes sure it ends with "/";
// "" stays "" (direct). Anything that is not an http(s) URL is refused.
func NormalizeMirror(m string) (string, error) {
	m = strings.TrimSpace(m)
	if m == "" {
		return "", nil
	}
	u, err := url.Parse(m)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", i18n.Error("镜像地址须是以 http:// 或 https:// 开头的完整地址")
	}
	if !strings.HasSuffix(m, "/") {
		m += "/"
	}
	return m, nil
}

// MirrorURL returns raw through mirror: the prefix followed by the full
// GitHub link, the form the common proxies take. Links to other hosts,
// an empty mirror and links already behind it come back unchanged.
func MirrorURL(mirror, raw string) string {
	mirror = strings.TrimSpace(mirror)
	if mirror == "" || raw == "" {
		return raw
	}
	if !strings.HasSuffix(mirror, "/") {
		mirror += "/"
	}
	if strings.HasPrefix(raw, mirror) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || !mirrorHosts[strings.ToLower(u.Host)] {
		return raw
	}
	return mirror + raw
}

// mirrored applies the configured mirror to raw.
func (s *Service) mirrored(raw string) string {
	s.mu.Lock()
	m := s.state.settings.DownloadMirror
	s.mu.Unlock()
	return MirrorURL(m, raw)
}

// mirroredGeo returns set's geo section with the mirror applied to the
// four download URLs, for the core's config (geox-url); set itself, and
// the settings file, keep the original links.
func mirroredGeo(set model.Settings) model.GeoSettings {
	g := set.Geo
	m := set.DownloadMirror
	g.GeoIP = MirrorURL(m, g.GeoIP)
	g.GeoSite = MirrorURL(m, g.GeoSite)
	g.MMDB = MirrorURL(m, g.MMDB)
	g.ASN = MirrorURL(m, g.ASN)
	return g
}

// TestMirrors implements API.TestMirrors: a HEAD request through every
// preset, directly (mirrors are for users without a working proxy), all
// at once; it returns when the slowest answered or timed out.
func (s *Service) TestMirrors() []model.MirrorTest {
	client := s.d.httpDirect(mirrorTestTimeout)
	out := make([]model.MirrorTest, len(MirrorPresets))
	var wg sync.WaitGroup
	for i, m := range MirrorPresets {
		out[i].URL = m
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			out[i].Ms, out[i].Error = headMirror(s.rootCtx, client, MirrorURL(m, mirrorProbeURL))
		}(i, m)
	}
	wg.Wait()
	for _, r := range out {
		if r.Error != "" {
			s.alog.Warnf("mirror %s: %s", r.URL, r.Error)
		} else {
			s.alog.Infof("mirror %s: %d ms", r.URL, r.Ms)
		}
	}
	return out
}

// headMirror measures one HEAD request; any 2xx/3xx answer counts.
func headMirror(ctx context.Context, c *http.Client, u string) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, mirrorTestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return -1, err.Error()
	}
	req.Header.Set("User-Agent", "Lanflow")
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return -1, i18n.T("超时")
		}
		return -1, err.Error()
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return -1, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return max(int(time.Since(start)/time.Millisecond), 1), ""
}
