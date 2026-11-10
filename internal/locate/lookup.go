package locate

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/oschwald/maxminddb-golang/v2"

	"lanflow/internal/geo"
	"lanflow/internal/model"
)

// databases holds the lazily opened MMDB readers; either may be nil.
type databases struct {
	geo *maxminddb.Reader
	asn *maxminddb.Reader
}

func (l *locator) openDBs() {
	if l.mmdbPath != "" {
		if r, err := maxminddb.Open(l.mmdbPath); err == nil {
			l.dbs.geo = r
		}
	}
	if l.asnPath != "" {
		if r, err := maxminddb.Open(l.asnPath); err == nil {
			l.dbs.asn = r
		}
	}
}

// lookup resolves an IP offline. Missing databases or unknown IPs leave
// the corresponding fields empty; the IP itself is always returned.
func (l *locator) lookup(ip string) model.EndpointInfo {
	ep := model.EndpointInfo{IP: ip}
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ep
	}
	addr = addr.Unmap()
	l.dbOnce.Do(l.openDBs)

	if l.dbs.geo != nil {
		if code := lookupCountry(l.dbs.geo, addr); code != "" {
			if p, ok := geo.Country(code); ok {
				// Only the country is known: show the country's name at
				// its main city's coordinates.
				p.City = geo.CountryName(code)
				ep.Place = &p
			} else {
				ep.Place = &model.Place{Country: code, City: code}
			}
		}
	}
	if l.dbs.asn != nil {
		num, org := lookupASN(l.dbs.asn, addr)
		if num != "" {
			ep.ASN = "AS" + num
		}
		ep.ISP = shortenOrg(org)
	}
	return ep
}

// lookupCountry returns the upper-case ISO code for addr, understanding
// both MaxMind-style records ({country:{iso_code}}) and MetaCubeX's
// Meta-geoip0 (a bare lower-case code or a list of codes and tags).
func lookupCountry(r *maxminddb.Reader, addr netip.Addr) string {
	res := r.Lookup(addr)
	if !res.Found() {
		return ""
	}
	if r.Metadata.DatabaseType != "Meta-geoip0" {
		var rec struct {
			Country struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
			RegisteredCountry struct {
				ISOCode string `maxminddb:"iso_code"`
			} `maxminddb:"registered_country"`
		}
		if err := res.Decode(&rec); err == nil {
			if rec.Country.ISOCode != "" {
				return strings.ToUpper(rec.Country.ISOCode)
			}
			if rec.RegisteredCountry.ISOCode != "" {
				return strings.ToUpper(rec.RegisteredCountry.ISOCode)
			}
		}
	}
	var any_ any
	if err := res.Decode(&any_); err != nil {
		return ""
	}
	return metaCountry(any_)
}

// metaCountry extracts a country code from a decoded Meta-geoip0 record
// (string or []any). Tags such as "cloudflare" are skipped.
func metaCountry(v any) string {
	switch rec := v.(type) {
	case string:
		if isISO2(rec) {
			return strings.ToUpper(rec)
		}
	case []any:
		for _, item := range rec {
			if s, ok := item.(string); ok && isISO2(s) {
				return strings.ToUpper(s)
			}
		}
	case []string:
		for _, s := range rec {
			if isISO2(s) {
				return strings.ToUpper(s)
			}
		}
	}
	return ""
}

func isISO2(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		c := s[i] | 0x20
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// lookupASN reads GeoLite2-ASN style records (also DB-IP compat) and the
// ipinfo free ASN database.
func lookupASN(r *maxminddb.Reader, addr netip.Addr) (num, org string) {
	res := r.Lookup(addr)
	if !res.Found() {
		return "", ""
	}
	if strings.HasPrefix(r.Metadata.DatabaseType, "ipinfo") {
		var rec struct {
			ASN  string `maxminddb:"asn"`
			Name string `maxminddb:"name"`
		}
		if res.Decode(&rec) == nil && rec.ASN != "" {
			return strings.TrimPrefix(rec.ASN, "AS"), rec.Name
		}
		return "", ""
	}
	var rec struct {
		Number uint   `maxminddb:"autonomous_system_number"`
		Org    string `maxminddb:"autonomous_system_organization"`
	}
	if res.Decode(&rec) != nil || rec.Number == 0 {
		return "", ""
	}
	return fmt.Sprint(rec.Number), rec.Org
}
