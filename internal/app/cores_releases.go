package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Release lists of the two channels, from the GitHub API:
//
//	正式版  GET /repos/MetaCubeX/mihomo/releases      — tags v1.x.y, prereleases skipped
//	预发布  GET /repos/MetaCubeX/mihomo/releases/tags/Prerelease-Alpha — one rolling
//	        release whose assets carry the version (mihomo-<os>-<arch>-alpha-<hash>.gz)
//
// Lists are cached for releaseCacheTTL in memory and on disk
// (cores/releases-<channel>.json), so a relaunch does not hit GitHub again
// and a rate-limited answer can fall back to the last list.

const (
	coreAlphaTag       = "Prerelease-Alpha"
	releaseCacheTTL    = time.Hour
	stableReleaseCount = 10
	releaseFetchLimit  = 30 // releases asked from the API; stable + prereleases are mixed
	releaseNotesLines  = 6  // lines of the notes kept as the excerpt
	releaseTimeout     = 30 * time.Second
	coreUpdateInterval = 24 * time.Hour
	coreUpdateTick     = time.Hour // how often coreUpdateLoop looks at the clock
	coreUpdateDelay    = 3 * time.Minute
)

// coreAPIBase is the GitHub API root of the repository; tests point it at
// a local server.
var coreAPIBase = "https://api.github.com/repos/MetaCubeX/mihomo"

// releaseCache is one channel's cached list.
type releaseCache struct {
	At   time.Time           `json:"at"`
	List []model.CoreRelease `json:"list"`
}

// ghRelease is the part of the GitHub release object used here.
type ghRelease struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	Assets      []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// coreAssetRe matches the plain build for one platform:
// mihomo-darwin-arm64-v1.19.33.gz, mihomo-windows-amd64-alpha-5a3a0ef.zip.
// Variants (compatible, v3, go120…) have another segment and do not match.
var coreAssetRe = regexp.MustCompile(`^mihomo-([a-z]+)-([a-z0-9]+)-(v\d+\.\d+\.\d+|alpha-[0-9a-f]{6,12})\.(gz|zip)$`)

// parseCoreAsset returns the version of an asset name when it is the plain
// build for goos/goarch.
func parseCoreAsset(name, goos, goarch string) (version string, ok bool) {
	m := coreAssetRe.FindStringSubmatch(name)
	if m == nil || m[1] != goos || m[2] != goarch {
		return "", false
	}
	want := "gz"
	if goos == "windows" {
		want = "zip"
	}
	if m[4] != want {
		return "", false
	}
	return m[3], true
}

// releasesFromAPI turns the GitHub objects of channel into releases for
// goos/goarch, newest first. For the stable channel every non-prerelease
// with a matching asset counts, at most stableReleaseCount; for the Alpha
// channel every matching asset of the Prerelease-Alpha release is a
// version of its own (normally one).
func releasesFromAPI(channel string, rels []ghRelease, goos, goarch string) []model.CoreRelease {
	var out []model.CoreRelease
	for _, r := range rels {
		if r.Draft {
			continue
		}
		isAlpha := r.Tag == coreAlphaTag
		switch channel {
		case model.CoreChannelStable:
			if r.Prerelease || isAlpha {
				continue
			}
		case model.CoreChannelAlpha:
			if !isAlpha {
				continue
			}
		}
		for _, a := range r.Assets {
			v, ok := parseCoreAsset(a.Name, goos, goarch)
			if !ok || channelOf(v) != channel {
				continue
			}
			if channel == model.CoreChannelStable && v != normalizeVersion(r.Tag) {
				continue
			}
			out = append(out, model.CoreRelease{
				Channel: channel, Version: v, Tag: r.Tag, PublishedAt: r.PublishedAt,
				Notes: notesExcerpt(r.Body), Asset: a.Name, Size: a.Size,
			})
			if channel == model.CoreChannelStable {
				break // one asset per release
			}
		}
		if channel == model.CoreChannelStable && len(out) >= stableReleaseCount {
			break
		}
	}
	return out
}

// notesExcerpt keeps the first few meaningful lines of a release body:
// Markdown headings, HTML, rules and the changelog link are dropped.
func notesExcerpt(body string) string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "**Full Changelog") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "*- "))
		if line == "" {
			continue
		}
		if len(line) > 120 {
			line = line[:120] + "…"
		}
		lines = append(lines, line)
		if len(lines) >= releaseNotesLines {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// coreAssetURL is the download link of version in channel (unmirrored).
func coreAssetURL(channel, version, asset string) string {
	tag := version
	if channel == model.CoreChannelAlpha {
		tag = coreAlphaTag
	}
	return coreReleaseBase + tag + "/" + asset
}

// releaseCachePath is cores/releases-<channel>.json.
func (s *Service) releaseCachePath(channel string) string {
	return filepath.Join(s.coresDir(), "releases-"+channel+".json")
}

// cachedReleases returns the cached list of channel and when it was
// fetched (zero when there is none), reading the disk copy once.
func (s *Service) cachedReleases(channel string) (releaseCache, bool) {
	s.cores.mu.Lock()
	defer s.cores.mu.Unlock()
	if s.cores.releases == nil {
		s.cores.releases = map[string]releaseCache{}
	}
	if rc, ok := s.cores.releases[channel]; ok {
		return rc, true
	}
	data, err := os.ReadFile(s.releaseCachePath(channel))
	if err != nil {
		return releaseCache{}, false
	}
	var rc releaseCache
	if json.Unmarshal(data, &rc) != nil || rc.List == nil {
		return releaseCache{}, false
	}
	s.cores.releases[channel] = rc
	return rc, true
}

// storeReleases caches list in memory and on disk.
func (s *Service) storeReleases(channel string, list []model.CoreRelease) {
	rc := releaseCache{At: s.d.now(), List: list}
	s.cores.mu.Lock()
	if s.cores.releases == nil {
		s.cores.releases = map[string]releaseCache{}
	}
	s.cores.releases[channel] = rc
	s.cores.mu.Unlock()
	if err := os.MkdirAll(s.coresDir(), 0o700); err == nil {
		if data, err := json.Marshal(rc); err == nil {
			_ = os.WriteFile(s.releaseCachePath(channel), data, 0o600)
		}
	}
}

// rateLimitError is GitHub's 403/429 with the quota used up.
type rateLimitError struct{ reset time.Time }

func (e rateLimitError) Error() string {
	if e.reset.IsZero() {
		return i18n.T("GitHub 接口的请求次数已达上限，请稍后再试")
	}
	wait := time.Until(e.reset).Round(time.Minute)
	if wait < time.Minute {
		wait = time.Minute
	}
	return fmt.Sprintf(i18n.T("GitHub 接口的请求次数已达上限，约 %d 分钟后恢复"), int(wait/time.Minute))
}

// githubJSON GETs a GitHub API URL and decodes it into out.
func githubJSON(ctx context.Context, c *http.Client, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Lanflow")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.StatusCode == http.StatusTooManyRequests {
			var reset time.Time
			if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && n > 0 {
				reset = time.Unix(n, 0)
			}
			return rateLimitError{reset: reset}
		}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(i18n.T("GitHub 返回 HTTP %d"), resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf(i18n.T("无法解析 GitHub 的回复：%v"), err)
	}
	return nil
}

// githubClients lists the clients to try: through the running core's
// proxy first (GitHub is often unreachable directly), then direct.
func (s *Service) githubClients(timeout time.Duration) []*http.Client {
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	s.mu.Unlock()
	var clients []*http.Client
	if running {
		clients = append(clients, s.d.httpVia(port, timeout))
	}
	return append(clients, s.d.httpDirect(timeout))
}

// fetchReleases asks GitHub for the list of channel.
func (s *Service) fetchReleases(ctx context.Context, channel string) ([]model.CoreRelease, error) {
	u := coreAPIBase + "/releases?per_page=" + strconv.Itoa(releaseFetchLimit)
	if channel == model.CoreChannelAlpha {
		u = coreAPIBase + "/releases/tags/" + coreAlphaTag
	}
	var lastErr error
	for _, c := range s.githubClients(releaseTimeout) {
		var rels []ghRelease
		if channel == model.CoreChannelAlpha {
			var one ghRelease
			lastErr = githubJSON(ctx, c, u, &one)
			rels = []ghRelease{one}
		} else {
			lastErr = githubJSON(ctx, c, u, &rels)
		}
		if lastErr == nil {
			list := releasesFromAPI(channel, rels, runtime.GOOS, runtime.GOARCH)
			if len(list) == 0 {
				return nil, errors.New(i18n.T("GitHub 没有返回版本号"))
			}
			return list, nil
		}
		var rl rateLimitError
		if errors.As(lastErr, &rl) || ctx.Err() != nil {
			break // another route will not help against the quota
		}
	}
	return nil, lastErr
}

// CoreReleases implements API.CoreReleases.
func (s *Service) CoreReleases(channel string, refresh bool) ([]model.CoreRelease, error) {
	channel, err := validChannel(channel)
	if err != nil {
		return nil, err
	}
	cached, hasCache := s.cachedReleases(channel)
	if hasCache && !refresh && s.d.now().Sub(cached.At) < releaseCacheTTL {
		return s.markInstalled(cached.List), nil
	}
	ctx, cancel := context.WithTimeout(s.rootCtx, releaseTimeout)
	defer cancel()
	list, err := s.fetchReleases(ctx, channel)
	if err != nil {
		if hasCache {
			// A stale list beats none: say so in the log and return it.
			s.alog.Warnf("core releases (%s): %v; using the list from %s", channel, err, cached.At.Format(time.RFC3339))
			return s.markInstalled(cached.List), nil
		}
		return nil, fmt.Errorf(i18n.T("检查更新失败：%v"), err)
	}
	s.storeReleases(channel, list)
	return s.markInstalled(list), nil
}

// markInstalled copies list with Installed set from the cores directory.
func (s *Service) markInstalled(list []model.CoreRelease) []model.CoreRelease {
	out := make([]model.CoreRelease, len(list))
	copy(out, list)
	for i := range out {
		out[i].Installed = s.isInstalled(out[i].Version)
	}
	return out
}

// currentCoreVersion is the version in use: the active installed version,
// else what the running (or bundled) core reported.
func (s *Service) currentCoreVersion() string {
	if v, _ := s.activeCore(); v != "" {
		return v
	}
	s.mu.Lock()
	v := s.state.st.CoreVersion
	s.mu.Unlock()
	if v == "" {
		_, v = s.bundledCore()
	}
	return normalizeVersion(v)
}

// isNewer reports whether latest is an update over current in channel.
func isNewer(channel, latest, current string) bool {
	if latest == "" || latest == current {
		return false
	}
	if channel == model.CoreChannelStable {
		if _, ok := semverParts(current); !ok {
			return true // from an alpha or unknown build, any release is a change worth offering
		}
		return compareSemver(latest, current) > 0
	}
	return true
}

// autoCoreCheck reports whether the core may be checked for updates on its
// own: Settings.CoreAutoCheck must be on, and Settings.DisableUpdateCheck
// (the app's "automatic update check" switch) off, since turning that off
// means no automatic network checks at all. A manual check from the
// settings page (API.CheckCoreUpdate) is never blocked by either.
func autoCoreCheck(set model.Settings) bool {
	return set.CoreAutoCheck && !set.DisableUpdateCheck
}

// CheckCoreUpdate implements API.CheckCoreUpdate.
func (s *Service) CheckCoreUpdate() (model.CoreUpdateInfo, error) {
	s.mu.Lock()
	channel := s.state.settings.CoreChannel
	s.mu.Unlock()
	channel, _ = validChannel(channel)
	list, err := s.CoreReleases(channel, false)
	if err != nil {
		return model.CoreUpdateInfo{Channel: channel}, err
	}
	info := model.CoreUpdateInfo{Channel: channel, Current: s.currentCoreVersion(), CheckedAt: s.d.now()}
	if len(list) > 0 {
		latest := list[0]
		info.Latest, info.Notes, info.Installed = latest.Version, latest.Notes, latest.Installed
		info.Available = isNewer(channel, latest.Version, info.Current) && !latest.Installed
	}
	s.cores.mu.Lock()
	s.cores.lastCheck = info.CheckedAt
	s.cores.mu.Unlock()
	return info, nil
}

// coreUpdateLoop checks for a new core daily while Settings.CoreAutoCheck
// is on and the app's own update check is not disabled (first a few
// minutes after launch), notifies, and installs and switches when
// CoreAutoInstall is on.
func (s *Service) coreUpdateLoop() {
	first := time.NewTimer(coreUpdateDelay)
	defer first.Stop()
	tick := time.NewTicker(coreUpdateTick)
	defer tick.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-first.C:
			s.runCoreUpdateCheck()
		case <-tick.C:
			s.runCoreUpdateCheck()
		}
	}
}

// runCoreUpdateCheck is one pass of coreUpdateLoop: skipped when automatic
// checks are off (autoCoreCheck), a check ran within coreUpdateInterval,
// or a download is in progress.
func (s *Service) runCoreUpdateCheck() {
	s.mu.Lock()
	set := s.state.settings
	busy := s.coreDLLocked().Active
	s.mu.Unlock()
	if !autoCoreCheck(set) || busy {
		return
	}
	s.cores.mu.Lock()
	last := s.cores.lastCheck
	s.cores.mu.Unlock()
	if !last.IsZero() && s.d.now().Sub(last) < coreUpdateInterval {
		return
	}
	info, err := s.CheckCoreUpdate()
	if err != nil {
		s.alog.Warnf("core update check: %v", err)
		return
	}
	if !info.Available {
		s.alog.Infof("core update check: %s is current (%s)", info.Current, info.Channel)
		return
	}
	s.alog.Infof("core update available: %s → %s (%s)", info.Current, info.Latest, info.Channel)
	if !set.CoreAutoInstall {
		s.notify(Notice{Kind: "toast", Title: i18n.T("有新版本 ") + info.Latest, Detail: fmt.Sprintf(i18n.T("Mihomo 内核 %s 可在「设置 › 内核」中安装；当前为 %s。"), info.Latest, info.Current)})
		return
	}
	if err := s.InstallCore(info.Channel, info.Latest); err != nil {
		s.notify(Notice{Kind: "error", Title: i18n.T("内核自动更新失败"), Detail: err.Error()})
		return
	}
	if err := s.UseCore(info.Latest); err != nil {
		s.notify(Notice{Kind: "error", Title: i18n.T("内核自动更新失败"), Detail: err.Error()})
		return
	}
	s.notify(Notice{Kind: "toast", Title: i18n.T("内核已更新到 ") + info.Latest, Detail: i18n.T("自动更新已切换到新版本内核；旧版本仍保留在已安装列表中。")})
}
