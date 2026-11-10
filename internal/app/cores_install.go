package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// InstallCore implements API.InstallCore: download into
// cores/<version>/, verify (mihomo -v and -t against the current runtime
// config), hash and record. It never switches; UseCore does.
func (s *Service) InstallCore(channel, version string) (err error) {
	channel, err = validChannel(channel)
	if err != nil {
		return err
	}
	version = normalizeVersion(version)
	if version == "" {
		list, err := s.CoreReleases(channel, false)
		if err != nil {
			return err
		}
		version = list[0].Version
	}
	if err := checkVersionName(version); err != nil {
		return err
	}
	if channelOf(version) != channel {
		return fmt.Errorf(i18n.T("版本 %s 不属于所选的更新通道。"), version)
	}
	if s.isInstalled(version) {
		return nil
	}

	s.mu.Lock()
	dl := s.coreDLLocked()
	if dl.Active {
		s.mu.Unlock()
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	*dl = model.CoreDownload{Active: true, Phase: phaseDownload, Version: version}
	s.mu.Unlock()
	s.em.emit(TopicCoreDownload)

	defer func() { s.logOp("install core "+version, err) }()
	err = s.installVersion(channel, version)
	s.updateCoreDL(func(dl *model.CoreDownload) {
		if err != nil {
			*dl = model.CoreDownload{Version: version, Error: err.Error()}
		} else {
			*dl = model.CoreDownload{Version: version, Done: version, Bytes: dl.Bytes, Total: dl.Total}
		}
	})
	if err == nil {
		s.refreshCores()
	}
	return err
}

// installVersion does the work of InstallCore; the progress record is
// already active.
func (s *Service) installVersion(channel, version string) error {
	asset, isZip, err := coreAsset(runtime.GOOS, runtime.GOARCH, version)
	if err != nil {
		return err
	}
	dir := s.coreVersionDir(version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf(i18n.T("无法创建内核目录 %s：%v"), dir, err)
	}
	final := s.coreVersionPath(version)
	archive := final + ".download"
	fresh := final + ".new"
	defer os.Remove(archive)
	defer os.Remove(fresh)
	cleanup := func() {
		os.Remove(archive)
		os.Remove(fresh)
		os.Remove(dir) // only when nothing else is in it
	}

	ctx, cancel := context.WithTimeout(s.rootCtx, coreDownloadTimeout)
	defer cancel()
	src := coreAssetURL(channel, version, asset)
	if err := s.fetchCoreArchive(ctx, s.mirrored(src), archive); err != nil {
		cleanup()
		return err
	}

	s.updateCoreDL(func(dl *model.CoreDownload) { dl.Phase = phaseVerify })
	if err := extractCore(archive, fresh, isZip); err != nil {
		cleanup()
		return fmt.Errorf(i18n.T("解压内核失败：%v"), err)
	}
	if err := os.Chmod(fresh, 0o755); err != nil {
		cleanup()
		return fmt.Errorf(i18n.T("无法设置内核的可执行权限：%v"), err)
	}
	got, err := s.d.core.Version(ctx, fresh)
	if err != nil {
		cleanup()
		return fmt.Errorf(i18n.T("下载的内核无法运行：%v"), err)
	}
	if !versionMatches(channel, version, got) {
		cleanup()
		return fmt.Errorf(i18n.T("下载的内核版本为 %s，与请求的 %s 不符，已放弃。"), got, version)
	}
	if _, err := os.Stat(s.runtime); err == nil {
		if err := s.d.core.Validate(ctx, fresh, s.workDir, s.runtime); err != nil {
			cleanup()
			return fmt.Errorf(i18n.T("新内核无法运行当前配置，已保留现有内核：%v"), err)
		}
	}

	s.updateCoreDL(func(dl *model.CoreDownload) { dl.Phase = phaseInstall })
	sum, err := fileSHA256(fresh)
	if err != nil {
		cleanup()
		return fmt.Errorf(i18n.T("无法安装内核文件：%v"), err)
	}
	if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
		cleanup()
		return fmt.Errorf(i18n.T("无法替换现有内核文件：%v"), err)
	}
	if err := os.Rename(fresh, final); err != nil {
		cleanup()
		return fmt.Errorf(i18n.T("无法安装内核文件：%v"), err)
	}
	meta := coreMeta{Version: version, Channel: channel, SHA256: sum, InstalledAt: s.d.now(), Source: src}
	if err := writeCoreMeta(dir, meta); err != nil {
		os.Remove(final)
		cleanup()
		return fmt.Errorf(i18n.T("无法安装内核文件：%v"), err)
	}
	s.alog.Infof("core %s installed from %s (sha256 %s)", version, src, sum[:12])
	return nil
}

// versionMatches compares what `mihomo -v` reported with the requested
// version: exact for releases; Alpha builds only have to be Alpha builds,
// since the hash in the asset name and the one in the binary may differ in
// length.
func versionMatches(channel, want, got string) bool {
	got = normalizeVersion(got)
	if got == want {
		return true
	}
	if channel != model.CoreChannelAlpha {
		return false
	}
	g := strings.TrimPrefix(got, "alpha-")
	w := strings.TrimPrefix(want, "alpha-")
	return strings.HasPrefix(got, "alpha") && (strings.HasPrefix(g, w) || strings.HasPrefix(w, g))
}
