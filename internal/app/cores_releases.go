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

	"lanflow/internal/fsx"
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
// and a rate-limited answer can fall back to the last list. The cache keeps
// GitHub's ETag: a refresh asks with If-None-Match, and an unchanged list
// comes back as 304 Not Modified, which GitHub does not count against the
// quota of 60 unauthenticated requests an hour.
//
// When GitHub says the quota is used up, no request goes out until the
// time it gives (X-RateLimit-Reset, or Retry-After for the secondary
// limits), and the automatic check (coreUpdateLoop) is scheduled for then;
// other failures back off from coreUpdateTick up to coreUpdateInterval.

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
	// coreUpdateMinWait is the shortest wait coreUpdateLoop schedules.
	coreUpdateMinWait = time.Minute
)

// coreAPIBase is the GitHub API root of the repository; tests point it at
// a local server.
var coreAPIBase = "https://api.github.com/repos/MetaCubeX/mihomo"

// releaseCache is one channel's cached list.
type releaseCache struct {
	At   time.Time           `json:"at"`
	List []model.CoreRelease `json:"list"`
	ETag string              `json:"etag,omitempty"` // GitHub's ETag of the list, for If-None-Match
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
func (s *Service) storeReleases(channel string, list []model.CoreRelease, etag string) {
	rc := releaseCache{At: s.d.now(), List: list, ETag: etag}
	s.cores.mu.Lock()
	if s.cores.releases == nil {
		s.cores.releases = map[string]releaseCache{}
	}
	s.cores.releases[channel] = rc
	s.cores.mu.Unlock()
	if data, err := json.Marshal(rc); err == nil {
		_ = fsx.WriteFile(s.releaseCachePath(channel), data, 0o600)
	}
}

// rateLimitError is GitHub's 403/429 with the quota used up. reset is
// when GitHub says requests are allowed again (zero when it did not say);
// the minutes in the message are counted from now each time it is shown.
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

// parseRateLimit reports whether a GitHub response says the quota is used
// up, and until when: a 429, or a 403 with X-RateLimit-Remaining 0 (the
// primary limit) or with Retry-After (the secondary limits). The later of
// Retry-After (seconds or an HTTP date) and X-RateLimit-Reset (Unix time)
// wins; zero when neither is given.
func parseRateLimit(status int, h http.Header, now time.Time) (reset time.Time, limited bool) {
	retryAfter := strings.TrimSpace(h.Get("Retry-After"))
	switch {
	case status == http.StatusTooManyRequests:
	case status == http.StatusForbidden && (h.Get("X-RateLimit-Remaining") == "0" || retryAfter != ""):
	default:
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && n > 0 {
		reset = time.Unix(n, 0)
	}
	if retryAfter != "" {
		var at time.Time
		if n, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && n >= 0 {
			at = now.Add(time.Duration(n) * time.Second)
		} else if t, err := http.ParseTime(retryAfter); err == nil {
			at = t
		}
		if at.After(reset) {
			reset = at
		}
	}
	return reset, true
}

// ghResult is what githubJSON learned besides the body.
type ghResult struct {
	ETag        string // the response's ETag ("" when none)
	NotModified bool   // 304: the etag sent still matches; out is untouched
}

// githubJSON GETs a GitHub API URL and decodes it into out. With etag set
// it asks If-None-Match, and a 304 comes back as NotModified. While an
// earlier answer said the quota is used up, it returns that rateLimitError
// without a request; a new rate-limit answer is remembered the same way.
func (s *Service) githubJSON(ctx context.Context, c *http.Client, u, etag string, out any) (ghResult, error) {
	if until := s.rateLimitedUntil(); !until.IsZero() {
		return ghResult{}, rateLimitError{reset: until}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ghResult{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Lanflow")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.Do(req)
	if err != nil {
		return ghResult{}, err
	}
	defer resp.Body.Close()
	if reset, limited := parseRateLimit(resp.StatusCode, resp.Header, s.d.now()); limited {
		s.setRateLimited(reset)
		return ghResult{}, rateLimitError{reset: reset}
	}
	res := ghResult{ETag: resp.Header.Get("ETag")}
	if resp.StatusCode == http.StatusNotModified && etag != "" {
		res.NotModified = true
		if res.ETag == "" {
			res.ETag = etag
		}
		return res, nil
	}
	if resp.StatusCode != http.StatusOK {
		return ghResult{}, fmt.Errorf(i18n.T("GitHub 返回 HTTP %d"), resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return ghResult{}, fmt.Errorf(i18n.T("无法解析 GitHub 的回复：%v"), err)
	}
	return res, nil
}

// rateLimitedUntil is when GitHub allows requests again after a rate-limit
// answer; zero when no limit is known to be in force.
func (s *Service) rateLimitedUntil() time.Time {
	s.cores.mu.Lock()
	defer s.cores.mu.Unlock()
	if !s.cores.limitedUntil.IsZero() && s.d.now().Before(s.cores.limitedUntil) {
		return s.cores.limitedUntil
	}
	return time.Time{}
}

// setRateLimited remembers a rate-limit answer. Without a reset time
// nothing is held back: the next request asks again.
func (s *Service) setRateLimited(reset time.Time) {
	s.cores.mu.Lock()
	s.cores.limitedUntil = reset
	s.cores.mu.Unlock()
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

// fetchReleases asks GitHub for the list of channel. With etag set an
// unchanged list comes back as notModified (and a nil list).
func (s *Service) fetchReleases(ctx context.Context, channel, etag string) (list []model.CoreRelease, res ghResult, err error) {
	u := coreAPIBase + "/releases?per_page=" + strconv.Itoa(releaseFetchLimit)
	if channel == model.CoreChannelAlpha {
		u = coreAPIBase + "/releases/tags/" + coreAlphaTag
	}
	var lastErr error
	for _, c := range s.githubClients(releaseTimeout) {
		var rels []ghRelease
		if channel == model.CoreChannelAlpha {
			var one ghRelease
			res, lastErr = s.githubJSON(ctx, c, u, etag, &one)
			rels = []ghRelease{one}
		} else {
			res, lastErr = s.githubJSON(ctx, c, u, etag, &rels)
		}
		if lastErr == nil {
			if res.NotModified {
				return nil, res, nil
			}
			list := releasesFromAPI(channel, rels, runtime.GOOS, runtime.GOARCH)
			if len(list) == 0 {
				return nil, ghResult{}, errors.New(i18n.T("GitHub 没有返回版本号"))
			}
			return list, res, nil
		}
		var rl rateLimitError
		if errors.As(lastErr, &rl) || ctx.Err() != nil {
			break // another route will not help against the quota
		}
	}
	return nil, ghResult{}, lastErr
}

// CoreReleases implements API.CoreReleases.
func (s *Service) CoreReleases(channel string, refresh bool) ([]model.CoreRelease, error) {
	list, fetchErr, err := s.coreReleases(channel, refresh)
	if fetchErr != nil && err == nil {
		var rl rateLimitError
		if errors.As(fetchErr, &rl) {
			s.alog.Debugf("core releases (%s): %v; using the cached list", channel, fetchErr)
		} else {
			s.alog.Warnf("core releases (%s): %v; using the cached list", channel, fetchErr)
		}
	}
	return list, err
}

// coreReleases is CoreReleases that also says when the list is an old one
// because GitHub could not be asked (fetchErr; err is nil then).
func (s *Service) coreReleases(channel string, refresh bool) (list []model.CoreRelease, fetchErr, err error) {
	channel, err = validChannel(channel)
	if err != nil {
		return nil, nil, err
	}
	cached, hasCache := s.cachedReleases(channel)
	if hasCache && !refresh && s.d.now().Sub(cached.At) < releaseCacheTTL {
		return s.markInstalled(cached.List), nil, nil
	}
	etag := ""
	if hasCache {
		etag = cached.ETag
	}
	ctx, cancel := s.ctrlCtx(nil, releaseTimeout)
	defer cancel()
	list, res, ferr := s.fetchReleases(ctx, channel, etag)
	if ferr != nil {
		if hasCache {
			// A stale list beats none; the caller logs fetchErr.
			return s.markInstalled(cached.List), ferr, nil
		}
		return nil, ferr, fmt.Errorf(i18n.T("检查更新失败：%v"), ferr)
	}
	if res.NotModified {
		list = cached.List
	}
	s.storeReleases(channel, list, res.ETag)
	return s.markInstalled(list), nil, nil
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
	info, fetchErr, err := s.checkCoreUpdate()
	if fetchErr != nil && err == nil {
		s.alog.Warnf("core update check: %v; using the cached list", fetchErr)
	}
	return info, err
}

// checkCoreUpdate compares the latest release with the core in use.
// fetchErr is set when GitHub could not be asked and the cached list was
// used instead.
func (s *Service) checkCoreUpdate() (info model.CoreUpdateInfo, fetchErr, err error) {
	s.mu.Lock()
	channel := s.state.settings.CoreChannel
	s.mu.Unlock()
	channel, _ = validChannel(channel)
	list, fetchErr, err := s.coreReleases(channel, false)
	if err != nil {
		return model.CoreUpdateInfo{Channel: channel}, fetchErr, err
	}
	info = model.CoreUpdateInfo{Channel: channel, Current: s.currentCoreVersion(), CheckedAt: s.d.now()}
	if len(list) > 0 {
		latest := list[0]
		info.Latest, info.Notes, info.Installed = latest.Version, latest.Notes, latest.Installed
		info.Available = isNewer(channel, latest.Version, info.Current) && !latest.Installed
	}
	if fetchErr == nil {
		s.cores.mu.Lock()
		s.cores.lastCheck = info.CheckedAt
		s.cores.mu.Unlock()
	}
	return info, fetchErr, nil
}

// coreUpdateLoop checks for a new core daily while Settings.CoreAutoCheck
// is on and the app's own update check is not disabled (first a few
// minutes after launch), notifies, and installs and switches when
// CoreAutoInstall is on. It wakes at least every coreUpdateTick, and at
// the retry time a failed check scheduled (coreCheckFailed) when that is
// sooner.
func (s *Service) coreUpdateLoop() {
	t := time.NewTimer(coreUpdateDelay)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			s.runCoreUpdateCheck()
			t.Reset(s.nextCoreCheckWait())
		}
	}
}

// nextCoreCheckWait is how long coreUpdateLoop sleeps: until a scheduled
// retry, at most coreUpdateTick, at least coreUpdateMinWait.
func (s *Service) nextCoreCheckWait() time.Duration {
	s.cores.mu.Lock()
	retry := s.cores.retryAt
	s.cores.mu.Unlock()
	wait := coreUpdateTick
	if !retry.IsZero() {
		if d := retry.Sub(s.d.now()); d < wait {
			wait = d
		}
	}
	return max(wait, coreUpdateMinWait)
}

// coreCheckFailed schedules the next automatic check after a failure: when
// GitHub's quota resets, else after a backoff that doubles from
// coreUpdateTick up to coreUpdateInterval. Only the first of consecutive
// failures is a warning; the rest are logged at debug level.
func (s *Service) coreCheckFailed(err error) {
	now := s.d.now()
	s.cores.mu.Lock()
	s.cores.failures++
	n := s.cores.failures
	backoff := coreUpdateInterval
	if n <= 8 && coreUpdateTick<<(n-1) < coreUpdateInterval {
		backoff = coreUpdateTick << (n - 1)
	}
	retry := now.Add(backoff)
	var rl rateLimitError
	if errors.As(err, &rl) && !rl.reset.IsZero() {
		retry = rl.reset.Add(time.Minute) // a little after the reset, not before
		if retry.Before(now) {
			retry = now
		}
	}
	s.cores.retryAt = retry
	s.cores.mu.Unlock()
	if n == 1 {
		s.alog.Warnf("core update check: %v; next try at %s", err, retry.Format(time.TimeOnly))
	} else {
		s.alog.Debugf("core update check (failed %d times in a row): %v; next try at %s", n, err, retry.Format(time.TimeOnly))
	}
}

// coreCheckSucceeded clears the failure schedule.
func (s *Service) coreCheckSucceeded() {
	s.cores.mu.Lock()
	n := s.cores.failures
	s.cores.failures = 0
	s.cores.retryAt = time.Time{}
	s.cores.mu.Unlock()
	if n > 1 {
		s.alog.Infof("core update check works again after %d failures", n)
	}
}

// runCoreUpdateCheck is one pass of coreUpdateLoop: skipped when automatic
// checks are off (autoCoreCheck), a check ran within coreUpdateInterval,
// a failed check scheduled a later retry, or a download is in progress.
func (s *Service) runCoreUpdateCheck() {
	s.mu.Lock()
	set := s.state.settings
	busy := s.coreDLLocked().Active
	s.mu.Unlock()
	if !autoCoreCheck(set) || busy {
		return
	}
	now := s.d.now()
	s.cores.mu.Lock()
	last, retry := s.cores.lastCheck, s.cores.retryAt
	s.cores.mu.Unlock()
	if !retry.IsZero() && now.Before(retry) {
		return
	}
	if retry.IsZero() && !last.IsZero() && now.Sub(last) < coreUpdateInterval {
		return
	}
	info, fetchErr, err := s.checkCoreUpdate()
	if fetchErr != nil {
		err = fetchErr // GitHub's own error (a cached list is no check either)
	}
	if err != nil {
		s.coreCheckFailed(err)
		return
	}
	s.coreCheckSucceeded()
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
