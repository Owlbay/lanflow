package geo

import (
	"os"
	"strings"
)

// LocalZone returns the IANA name of the system time zone ("Asia/Shanghai"),
// or "" when it cannot be told. Go's time.Local is only ever named
// "Local", so the zone is read from where the OS keeps it: the TZ
// variable first, then the registry's TimeZoneKeyName on Windows (mapped
// through FromWindowsZone), then the /etc/localtime link on Unix. The
// overview places 「你」 at this zone's city, marked 「推测」, until the exit
// probe has measured the real location (docs/design.md §5.1 页面状态).
func LocalZone() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		return strings.TrimPrefix(tz, ":")
	}
	if z := osLocalZone(); z != "" {
		return z
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	// Some Linux systems have a plain file rather than a link, and name
	// the zone in /etc/timezone instead.
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if z := strings.TrimSpace(string(b)); z != "" {
			return z
		}
	}
	return ""
}
