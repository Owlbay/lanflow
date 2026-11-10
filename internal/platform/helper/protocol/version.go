package protocol

import (
	"strconv"
	"strings"
)

// CompareVersions compares two helper versions of the form
// [v]MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]. It returns -1, 0 or 1 and
// ok=false when either side is not such a version (for example "dev"), in
// which case callers must not draw conclusions. Prerelease strings are
// compared lexically and sort before the release.
func CompareVersions(a, b string) (cmp int, ok bool) {
	pa, prea, oka := parseVersion(a)
	pb, preb, okb := parseVersion(b)
	if !oka || !okb {
		return 0, false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	switch {
	case prea == preb:
		return 0, true
	case prea == "":
		return 1, true
	case preb == "":
		return -1, true
	case prea < preb:
		return -1, true
	default:
		return 1, true
	}
}

func parseVersion(s string) (nums [3]uint64, pre string, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	s, pre, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// ParseVersionLine extracts the version from the output of
// `lanflow-helper version` ("lanflow-helper 1.2.3 (protocol 1)").
func ParseVersionLine(out string) (version string, ok bool) {
	f := strings.Fields(out)
	if len(f) < 2 || f[0] != "lanflow-helper" {
		return "", false
	}
	return f[1], true
}
