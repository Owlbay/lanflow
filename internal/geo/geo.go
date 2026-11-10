// Package geo knows places: node-name parsing, country/city tables,
// Cloudflare colo (IATA) → city, and a coarse land bitmap for the map.
//
// Exported signatures are a stable contract. All data tables are our own
// (written from public knowledge, cities' coordinates to two
// decimals). The land bitmap must be generated from Natural Earth 1:110m
// land (public domain) by tools/landgrid and committed as land_data.go.
package geo

import "lanflow/internal/model"

// FromName guesses a node's place from its name, e.g. "🇭🇰 香港 03 IPLC",
// "JP-Tokyo-02", "美国 圣何塞", "Relay-7 专线" (→ false). City beats
// country; flag emoji name a country; Chinese names match as substrings;
// English names/ISO codes match whole ASCII tokens case-insensitively
// ("IPLC" is not PL, "AUS" is not US); "100GB" is not GB; mainland China
// is used only when nothing else matches (relays enter there).
//
// When only a country is recognised, City holds the country's Chinese name
// ("美国") and Lat/Lon those of its main city, so the map can still place it.
func FromName(name string) (model.Place, bool) { return fromName(name) }

// FromTrace builds a place from a Cloudflare trace: loc (ISO country) and
// colo (IATA airport code of the data centre). Falls back to the country's
// main city when the colo is unknown.
func FromTrace(loc, colo string) (model.Place, bool) { return fromTrace(loc, colo) }

// Country returns the place of a country's main city, by ISO code.
func Country(code string) (model.Place, bool) { return country(code) }

// CountryName returns the Chinese name of an ISO country code ("" if unknown).
func CountryName(code string) string { return countryName(code) }

// Flag returns the flag emoji for an ISO country code ("" if invalid).
func Flag(code string) string { return flag(code) }

// FromTimeZone guesses the user's city from an IANA zone (e.g. "Asia/Shanghai").
func FromTimeZone(zone string) (model.Place, bool) { return fromTimeZone(zone) }

// OnLand reports whether a 1°×1° cell containing (lat, lon) is land.
func OnLand(lat, lon float64) bool { return onLand(lat, lon) }
