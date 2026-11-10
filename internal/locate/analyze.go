package locate

import (
	"math"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// analyze fills Entry, Path and PathNote. Priority, highest first:
//
//  1. PathSuspicious: the measured exit contradicts itself (IP country vs
//     Cloudflare colo country), contradicts the node's name, or the probe
//     round trip is below the physical minimum between entry and exit.
//  2. PathChain: chainHops > 1 (client-side relay / dialer-proxy).
//  3. PathRelay: entry and exit are known and in different places.
//  4. PathDirect: entry and exit are the same IP or the same place.
//
// Without entry information (and none of the above) Path stays
// PathUnknown rather than claiming a direct connection.
func (l *locator) analyze(info *model.ExitInfo, entryIP string, chainHops int) {
	if info == nil {
		return
	}
	info.Entry = nil
	info.ChainHops = chainHops
	info.Path, info.PathNote = model.PathUnknown, ""

	if entryIP != "" {
		ep := l.resolve(entryIP)
		info.Entry = &ep
	}

	if info.Source == model.LocateProbe {
		if note := l.suspicion(info); note != "" {
			info.Path, info.PathNote = model.PathSuspicious, note
			return
		}
	}
	if chainHops > 1 {
		info.Path = model.PathChain
		info.PathNote = i18n.Tf("%d 跳链式代理", chainHops)
		return
	}
	if info.Entry == nil {
		return
	}
	exitIP := info.Exit.IP
	if exitIP != "" && exitIP == info.Entry.IP {
		info.Path = model.PathDirect
		return
	}
	entryPlace, exitPlace := info.Entry.Place, info.Place
	if entryPlace == nil || exitPlace == nil {
		return
	}
	if samePlace(*entryPlace, *exitPlace) {
		info.Path = model.PathDirect
		return
	}
	info.Path = model.PathRelay
	info.PathNote = i18n.Tf("入口 %s → 落地 %s", describe(*info.Entry), exitPlace.City)
}

// suspicion returns a note when a probed exit looks inconsistent, else "".
func (l *locator) suspicion(info *model.ExitInfo) string {
	// IP registration vs the data centre that actually answered.
	if info.Colo != "" && info.Exit.Place != nil {
		if coloPlace, ok := geo.FromTrace("", info.Colo); ok && coloPlace.Country != info.Exit.Place.Country {
			return i18n.Tf("IP 归属%s，实际经%s机房", geo.CountryName(info.Exit.Place.Country), coloPlace.City)
		}
	}
	// Node name vs measurement. A name that only says mainland China
	// describes the entry, not the exit, so it is not a contradiction.
	if info.Place != nil {
		if named, ok := geo.FromName(info.Node); ok && named.Country != "CN" && named.Country != info.Place.Country {
			return i18n.Tf("名称为%s，实测出口在%s", geo.CountryName(named.Country), info.Place.City)
		}
	}
	// Physics: the probe round trip cannot be shorter than light needs for
	// entry → exit and back. Only meaningful with city-level places.
	if rtt := l.probeRTT(info.Node); rtt > 0 && info.Entry != nil && info.Entry.Place != nil && info.Place != nil {
		a, b := *info.Entry.Place, *info.Place
		if cityLevel(a) && cityLevel(b) && !samePlace(a, b) {
			if floor := minRTT(a, b); rtt < floor {
				return i18n.Tf("延迟 %d ms 低于 %s→%s 的物理下限 %d ms", rtt.Milliseconds(), a.City, b.City, floor.Milliseconds())
			}
		}
	}
	return ""
}

// describe renders an endpoint as "城市 运营商" for notes.
func describe(ep model.EndpointInfo) string {
	s := ""
	if ep.Place != nil {
		s = ep.Place.City
	}
	if ep.ISP != "" {
		if s != "" {
			s += " "
		}
		s += ep.ISP
	}
	if s == "" {
		s = ep.IP
	}
	return s
}

// cityLevel reports whether p names a city rather than just a country.
func cityLevel(p model.Place) bool {
	return p.City != "" && p.City != geo.CountryName(p.Country)
}

// samePlace treats two places as equal when they are in the same country
// and either names the same city or one of them is only country-level.
func samePlace(a, b model.Place) bool {
	if a.Country != b.Country {
		return false
	}
	return a.City == b.City || !cityLevel(a) || !cityLevel(b)
}

// lightSpeedKmPerMs is the speed of light in optical fibre, roughly
// 200 km per millisecond.
const lightSpeedKmPerMs = 200.0

// minRTT is the physical lower bound of a round trip between two places:
// twice the great-circle distance at fibre speed.
func minRTT(a, b model.Place) time.Duration {
	ms := 2 * distanceKm(a.Lat, a.Lon, b.Lat, b.Lon) / lightSpeedKmPerMs
	return time.Duration(ms * float64(time.Millisecond))
}

// distanceKm is the haversine great-circle distance.
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	s := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(s)))
}
