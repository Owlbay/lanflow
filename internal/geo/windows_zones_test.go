package geo

import (
	"strings"
	"testing"
)

// TestFromWindowsZone checks the Windows → IANA table on the zones a
// Windows user of the app is most likely to be in, and the tolerance for
// what the registry hands back.
func TestFromWindowsZone(t *testing.T) {
	cases := map[string]string{
		"China Standard Time":            "Asia/Shanghai",
		"Taipei Standard Time":           "Asia/Taipei",
		"Tokyo Standard Time":            "Asia/Tokyo",
		"Korea Standard Time":            "Asia/Seoul",
		"Singapore Standard Time":        "Asia/Singapore",
		"SE Asia Standard Time":          "Asia/Bangkok",
		"India Standard Time":            "Asia/Kolkata",
		"Arabian Standard Time":          "Asia/Dubai",
		"Russian Standard Time":          "Europe/Moscow",
		"GMT Standard Time":              "Europe/London",
		"Romance Standard Time":          "Europe/Paris",
		"W. Europe Standard Time":        "Europe/Berlin",
		"Central European Standard Time": "Europe/Warsaw",
		"FLE Standard Time":              "Europe/Kyiv",
		"Eastern Standard Time":          "America/New_York",
		"Central Standard Time":          "America/Chicago",
		"Mountain Standard Time":         "America/Denver",
		"Pacific Standard Time":          "America/Los_Angeles",
		"E. South America Standard Time": "America/Sao_Paulo",
		"AUS Eastern Standard Time":      "Australia/Sydney",
		"New Zealand Standard Time":      "Pacific/Auckland",
		"South Africa Standard Time":     "Africa/Johannesburg",
		"UTC":                            "Etc/UTC",
		// Registry strings may carry NULs and spaces; case is forgiven.
		"China Standard Time\x00\x00": "Asia/Shanghai",
		"  Tokyo Standard Time  ":     "Asia/Tokyo",
		"pacific standard time":       "America/Los_Angeles",
	}
	for in, want := range cases {
		got, ok := FromWindowsZone(in)
		if !ok || got != want {
			t.Errorf("FromWindowsZone(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "Local", "Asia/Shanghai", "Nowhere Standard Time"} {
		if z, ok := FromWindowsZone(in); ok {
			t.Errorf("FromWindowsZone(%q) = %q, want no match", in, z)
		}
	}
}

// TestWindowsZonesResolveToCities: every entry is an IANA zone, and the
// zones a user of the app is likely to be in resolve to a city, so a
// Windows user gets a guessed 「你」; a Windows key name fed straight to
// FromTimeZone works too. (Zones of small cities the city table does not
// know, Pacific/Chatham say, give no place, as on Unix.)
func TestWindowsZonesResolveToCities(t *testing.T) {
	for win, iana := range windowsZones {
		if !strings.Contains(iana, "/") {
			t.Errorf("%q maps to %q, not an IANA zone", win, iana)
		}
	}
	for _, win := range []string{
		"China Standard Time", "Taipei Standard Time", "Tokyo Standard Time", "Korea Standard Time",
		"Singapore Standard Time", "SE Asia Standard Time", "India Standard Time", "Arabian Standard Time",
		"Russian Standard Time", "GMT Standard Time", "Romance Standard Time", "W. Europe Standard Time",
		"Central European Standard Time", "Central Europe Standard Time", "FLE Standard Time", "GTB Standard Time",
		"Eastern Standard Time", "Central Standard Time", "Mountain Standard Time", "Pacific Standard Time",
		"US Mountain Standard Time", "Atlantic Standard Time", "E. South America Standard Time", "Argentina Standard Time",
		"Pacific SA Standard Time", "Central Standard Time (Mexico)", "AUS Eastern Standard Time", "W. Australia Standard Time",
		"Cen. Australia Standard Time", "E. Australia Standard Time", "New Zealand Standard Time", "Hawaiian Standard Time",
		"South Africa Standard Time", "Egypt Standard Time", "E. Africa Standard Time", "W. Central Africa Standard Time",
		"Turkey Standard Time", "Israel Standard Time", "Iran Standard Time", "Arab Standard Time", "Pakistan Standard Time",
		"Bangladesh Standard Time", "Myanmar Standard Time", "Ulaanbaatar Standard Time", "Nepal Standard Time",
		"Sri Lanka Standard Time", "West Asia Standard Time", "Ekaterinburg Standard Time", "Vladivostok Standard Time",
	} {
		iana, ok := FromWindowsZone(win)
		if !ok {
			t.Errorf("%q is not in the table", win)
			continue
		}
		if _, ok := FromTimeZone(iana); !ok {
			t.Errorf("%q → %q resolves to no city", win, iana)
		}
	}
	p, ok := FromTimeZone("China Standard Time")
	if !ok || p.City != "上海" {
		t.Errorf("FromTimeZone(China Standard Time) = %q, %v", p.City, ok)
	}
	if _, ok := FromTimeZone("UTC"); ok {
		t.Error("UTC must give no city")
	}
}

// TestLocalZoneFromTZ: the TZ variable wins over everything else, and the
// result feeds FromTimeZone.
func TestLocalZoneFromTZ(t *testing.T) {
	t.Setenv("TZ", ":Asia/Tokyo")
	if z := LocalZone(); z != "Asia/Tokyo" {
		t.Errorf("LocalZone() = %q with TZ set", z)
	}
	p, ok := FromTimeZone(LocalZone())
	if !ok || p.City != "东京" {
		t.Errorf("place of the local zone: %q, %v", p.City, ok)
	}
}
