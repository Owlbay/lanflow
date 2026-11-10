package app

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Downloading the official Mihomo core (docs/design.md:277 「下载内核」 and
// :519 「更新」). Assets follow scripts/fetch-core.sh: the plain amd64/arm64
// build, .gz on macOS/Linux and .zip on Windows. The archive's SHA-256 is
// checked first (coresums.go: pinned for pinnedCoreVersion, else the
// asset digest from the GitHub API); only then is the binary run, to see
// that `mihomo -v` reports the requested version and that `mihomo -t`
// accepts the current runtime config before it replaces the active core.

const (
	// pinnedCoreVersion is the release DownloadCore("") installs; keep in
	// sync with PINNED_VERSION in scripts/fetch-core.sh, and its checksums
	// with pinnedCoreSHA256 (coresums.go).
	pinnedCoreVersion = "v1.19.33"

	coreDownloadTimeout = 15 * time.Minute
	maxCoreArchive      = 256 << 20 // refuse anything larger (a real core is ~20 MB)
	progressStep        = 256 << 10 // emit progress at most every 256 KB
)

// model.CoreDownload.Phase values (env.CoreDownloadText words them).
const (
	phaseDownload = "下载"
	phaseVerify   = "校验"
	phaseInstall  = "安装"
	phaseStart    = "启动"
)

// coreReleaseBase is where release assets live; tests point it at a local server.
var coreReleaseBase = "https://github.com/MetaCubeX/mihomo/releases/download/"

// coreAsset returns the official asset name for goos/goarch and version,
// and whether it is a zip (Windows) rather than a gzip stream.
func coreAsset(goos, goarch, version string) (name string, isZip bool, err error) {
	switch goos + "/" + goarch {
	case "darwin/arm64", "darwin/amd64", "linux/arm64", "linux/amd64":
		return fmt.Sprintf("mihomo-%s-%s-%s.gz", goos, goarch, version), false, nil
	case "windows/amd64", "windows/arm64":
		return fmt.Sprintf("mihomo-%s-%s-%s.zip", goos, goarch, version), true, nil
	}
	return "", false, fmt.Errorf(i18n.T("Mihomo 没有提供 %s/%s 平台的官方内核，请手动选择内核文件"), goos, goarch)
}

// normalizeVersion gives "v1.19.33" for "1.19.33" / " v1.19.33 ".
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && v[0] >= '0' && v[0] <= '9' {
		v = "v" + v
	}
	return v
}

// managedCoreDir is where older versions kept the downloaded core
// (<DataDir>/core/bin); migrateLegacyCore (cores.go) moves it.
func (s *Service) managedCoreDir() string { return filepath.Join(s.workDir, "bin") }

// managedCorePath is the legacy downloaded core binary.
func (s *Service) managedCorePath() string {
	name := "mihomo"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(s.managedCoreDir(), name)
}

// coreDLLocked returns the progress record; mu must be held.
func (s *Service) coreDLLocked() *model.CoreDownload {
	if s.d.coreDL == nil {
		s.d.coreDL = &model.CoreDownload{}
	}
	return s.d.coreDL
}

// CoreDownload returns the progress of the current or last DownloadCore.
func (s *Service) CoreDownload() model.CoreDownload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.coreDLLocked()
}

// updateCoreDL changes the progress record and tells the UI.
func (s *Service) updateCoreDL(fn func(dl *model.CoreDownload)) {
	s.mu.Lock()
	fn(s.coreDLLocked())
	s.mu.Unlock()
	s.em.emit(TopicCoreDownload)
}

// DownloadCore implements API.DownloadCore: InstallCore followed by
// UseCore, kept for the 「下载内核」 button and older callers.
func (s *Service) DownloadCore(version string) error {
	version = normalizeVersion(version)
	if version == "" {
		version = pinnedCoreVersion
	}
	if err := s.InstallCore(channelOf(version), version); err != nil {
		return err
	}
	return s.UseCore(version)
}

// fetchCoreArchive downloads url to path, reporting progress. A running
// core's proxy is tried first (GitHub is often unreachable directly), then
// a direct connection; without a running core only the direct route exists.
func (s *Service) fetchCoreArchive(ctx context.Context, url, path string) error {
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	s.mu.Unlock()

	var clients []*http.Client
	if running {
		clients = append(clients, s.d.httpVia(port, coreDownloadTimeout))
	}
	clients = append(clients, s.d.httpDirect(coreDownloadTimeout))

	var lastErr error
	for _, c := range clients {
		lastErr = s.downloadTo(ctx, c, url, path)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	if running {
		return fmt.Errorf(i18n.T("下载内核失败：%v。已尝试通过代理和直连下载，请检查网络后重试。"), lastErr)
	}
	return fmt.Errorf(i18n.T("下载内核失败：%v。当前内核未运行，只能直连 GitHub；请检查网络后重试，或点击「选择内核文件」使用本机已有的内核。"), lastErr)
}

// downloadTo streams one HTTP response into path, updating Bytes/Total.
func (s *Service) downloadTo(ctx context.Context, c *http.Client, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Lanflow")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return errors.New(i18n.T("GitHub 上没有这个版本的内核（HTTP 404）"))
		}
		return fmt.Errorf(i18n.T("GitHub 返回 HTTP %d"), resp.StatusCode)
	}
	if resp.ContentLength > maxCoreArchive {
		return fmt.Errorf(i18n.T("文件过大（%d 字节），不像是 Mihomo 内核"), resp.ContentLength)
	}
	s.updateCoreDL(func(dl *model.CoreDownload) { dl.Bytes, dl.Total = 0, max(resp.ContentLength, 0) })

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf(i18n.T("无法写入 %s：%v"), path, err)
	}
	pr := &progressReader{r: io.LimitReader(resp.Body, maxCoreArchive+1), report: func(n int64) {
		s.updateCoreDL(func(dl *model.CoreDownload) { dl.Bytes = n })
	}}
	n, err := io.Copy(f, pr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf(i18n.T("保存下载内容失败：%v"), err)
	}
	if n > maxCoreArchive {
		os.Remove(path)
		return errors.New(i18n.T("文件过大，不像是 Mihomo 内核"))
	}
	if n == 0 {
		os.Remove(path)
		return errors.New(i18n.T("下载到的文件是空的"))
	}
	s.updateCoreDL(func(dl *model.CoreDownload) { dl.Bytes = n; dl.Total = n })
	return nil
}

// progressReader counts bytes and reports every progressStep.
type progressReader struct {
	r      io.Reader
	n      int64
	last   int64
	report func(n int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.n-p.last >= progressStep || (err != nil && p.n != p.last) {
		p.last = p.n
		p.report(p.n)
	}
	return n, err
}

// extractCore writes the mihomo binary inside archive to dst.
func extractCore(archive, dst string, isZip bool) error {
	var src io.ReadCloser
	if isZip {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return fmt.Errorf(i18n.T("不是有效的 zip 文件：%v"), err)
		}
		defer zr.Close()
		var found *zip.File
		for _, f := range zr.File {
			base := filepath.Base(f.Name)
			if !f.FileInfo().IsDir() && strings.HasPrefix(base, "mihomo") && strings.HasSuffix(base, ".exe") {
				found = f
				break
			}
		}
		if found == nil {
			return errors.New(i18n.T("压缩包里没有 mihomo*.exe"))
		}
		if found.UncompressedSize64 > maxCoreArchive {
			return errors.New(i18n.T("压缩包里的文件过大"))
		}
		rc, err := found.Open()
		if err != nil {
			return err
		}
		src = rc
	} else {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf(i18n.T("不是有效的 gzip 文件：%v"), err)
		}
		src = gz
	}
	defer src.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(src, maxCoreArchive+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	if n == 0 || n > maxCoreArchive {
		os.Remove(dst)
		return errors.New(i18n.T("解压出的内核大小异常"))
	}
	return nil
}

// gzipBytes compresses data (used by tests and kept here so the archive
// format has one definition).
func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}
