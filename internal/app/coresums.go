package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Checksums of downloaded cores. A downloaded archive is compared with a
// SHA-256 known before the download starts, and only a match is extracted,
// run (`mihomo -v`, `-t`) or installed:
//
//   - pinnedCoreVersion: the sums below, taken from the asset digests of the
//     official release (github.com/MetaCubeX/mihomo, release v1.19.33).
//     scripts/fetch-core.sh carries the same table; a test keeps the two
//     in sync.
//   - any other version (stable or Alpha): the `digest` field GitHub's
//     release API reports for the asset, asked from api.github.com itself
//     (coreAPIBase), never through the download mirror. An asset without a
//     digest is refused.
//
// The archive itself may come through the mirror; the check makes that safe.

// pinnedCoreSHA256 maps the official assets of pinnedCoreVersion to the
// SHA-256 of the archive (.gz / .zip) as published.
var pinnedCoreSHA256 = map[string]string{
	"mihomo-darwin-amd64-v1.19.33.gz":   "551ae54ef8d77a5e21f7a8185a59b441fb13a0fb0514fcdef5ca2816f6f50015",
	"mihomo-darwin-arm64-v1.19.33.gz":   "060b64b0f2c9a310831db0fe4adb2878de67bfa606c311813c1f13966aa504b6",
	"mihomo-linux-amd64-v1.19.33.gz":    "132f6f3edf9a65e62f04853aed3358472e0b2b7666daa8419edfa2ddf5bb6323",
	"mihomo-linux-arm64-v1.19.33.gz":    "ba408b317080ee265c46cb923a50018c1b82cbd708551cdd46a1e3e307396187",
	"mihomo-windows-amd64-v1.19.33.zip": "6ab2180875f9f6fff8b19386d760daec257f66b45f72b3afc650634272fa837a",
	"mihomo-windows-arm64-v1.19.33.zip": "a2a26869a14634c49f3fbcecc593b498ed8591fbdd7616b24475b3d60b148da3",
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ghDigestRelease is the part of a GitHub release object holding the
// asset digests ("sha256:<hex>", present for assets uploaded since 2025).
type ghDigestRelease struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	} `json:"assets"`
}

// coreDigest returns the expected SHA-256 (lowercase hex) of asset; a
// variable so tests can serve fake archives.
var coreDigest = (*Service).expectedCoreSHA256

// expectedCoreSHA256 looks up the SHA-256 asset of version must have.
func (s *Service) expectedCoreSHA256(ctx context.Context, channel, version, asset string) (string, error) {
	if version == pinnedCoreVersion {
		if sum, ok := pinnedCoreSHA256[asset]; ok {
			return sum, nil
		}
	}
	tag := version
	if channel == model.CoreChannelAlpha {
		tag = coreAlphaTag
	}
	// coreAPIBase is api.github.com: the digest must not come from the
	// mirror that may serve the archive, so s.mirrored is not applied.
	u := coreAPIBase + "/releases/tags/" + url.PathEscape(tag)
	var lastErr error
	for _, c := range s.githubClients(releaseTimeout) {
		var rel ghDigestRelease
		lastErr = githubJSON(ctx, c, u, &rel)
		if lastErr == nil {
			return digestOf(rel, asset)
		}
		var rl rateLimitError
		if errors.As(lastErr, &rl) || ctx.Err() != nil {
			break
		}
	}
	return "", fmt.Errorf(i18n.T("无法从 GitHub 取得内核的 SHA-256 校验值，未下载：%v"), lastErr)
}

// digestOf returns the hex SHA-256 GitHub reports for asset in rel.
func digestOf(rel ghDigestRelease, asset string) (string, error) {
	for _, a := range rel.Assets {
		if a.Name != asset {
			continue
		}
		sum, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(a.Digest)), "sha256:")
		if !ok || !sha256Hex.MatchString(sum) {
			return "", fmt.Errorf(i18n.T("GitHub 没有提供 %s 的 SHA-256 校验值，为安全起见不安装这个版本；请换一个版本，或点击「选择内核文件」使用自己核对过的内核。"), asset)
		}
		return sum, nil
	}
	return "", fmt.Errorf(i18n.T("GitHub 上的这个版本没有 %s"), asset)
}

// verifyCoreArchive checks the downloaded archive at path against want.
func verifyCoreArchive(path, asset, want string) error {
	got, err := fileSHA256(path)
	if err != nil {
		return fmt.Errorf(i18n.T("无法计算下载文件的 SHA-256：%v"), err)
	}
	if got != want {
		return fmt.Errorf(i18n.T("内核文件 %s 校验失败：SHA-256 为 %s，应为 %s。文件可能在下载途中（例如经镜像）被替换，已丢弃，没有运行或安装。"), asset, got, want)
	}
	return nil
}
