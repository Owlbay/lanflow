package geo

import (
	"strings"
	"sync"
	"unicode"

	"lanflow/internal/model"
)

// Match priority tiers: higher wins. Mainland China (entry side of relays)
// only wins when nothing else matched.
const (
	tierCountryCN = iota
	tierCityCN
	tierCountry
	tierCity
)

type matchKind uint8

const (
	kindCountry matchKind = iota
	kindCity
)

// hit is one alias occurrence inside a node name.
type hit struct {
	start, end int // byte span in the name
	kind       matchKind
	idx        int   // index into countries / cities
	alts       []int // other city indices sharing the same alias (ambiguous Chinese names)
}

// cjkAlias is an alias matched as a substring (contains non-Latin letters).
type cjkAlias struct {
	text string
	kind matchKind
	idx  int
}

// tokenAlias is an alias matched as a sequence of whole Latin tokens.
type tokenAlias struct {
	tokens []string
	exact  bool // case-sensitive
	kind   matchKind
	idx    int
}

type token struct {
	text       string
	start, end int
	digits     bool
}

var (
	indexOnce    sync.Once
	cjkAliases   []cjkAlias
	tokenAliases map[string][]tokenAlias // key: lower-case first token
	countryByISO map[string]int
	cityByName   map[string]int
	cityByColo   map[string]int
)

func buildIndex() {
	countryByISO = make(map[string]int, len(countries))
	cityByName = make(map[string]int, len(cities))
	cityByColo = make(map[string]int, len(cities)*2)
	tokenAliases = make(map[string][]tokenAlias)

	add := func(alias string, kind matchKind, idx int) {
		exact := false
		if strings.HasPrefix(alias, "=") {
			exact = true
			alias = alias[1:]
		}
		if isLatin(alias) {
			toks := tokenize(alias)
			if len(toks) == 0 {
				return
			}
			texts := make([]string, len(toks))
			for i, t := range toks {
				texts[i] = t.text
			}
			key := strings.ToLower(texts[0])
			tokenAliases[key] = append(tokenAliases[key], tokenAlias{texts, exact, kind, idx})
			return
		}
		cjkAliases = append(cjkAliases, cjkAlias{alias, kind, idx})
	}

	for i, c := range countries {
		countryByISO[c.Code] = i
		add(c.Name, kindCountry, i)
		code := c.Code
		if caseSensitiveCodes[code] {
			code = "=" + code
		}
		add(code, kindCountry, i)
		for _, a := range c.Aliases {
			add(a, kindCountry, i)
		}
	}
	for i, c := range cities {
		cityByName[c.Name] = i
		add(c.Name, kindCity, i)
		for _, a := range c.Aliases {
			add(a, kindCity, i)
		}
		for _, colo := range c.Colos {
			if _, dup := cityByColo[colo]; !dup {
				cityByColo[colo] = i
			}
			if !coloOnly[colo] {
				add("="+colo, kindCity, i)
			}
		}
	}
}

func ensureIndex() { indexOnce.Do(buildIndex) }

// isLatin reports whether s has no letters outside the Latin range, i.e. it
// is matched token-wise rather than as a substring.
func isLatin(s string) bool {
	for _, r := range s {
		if r >= 0x250 && unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

func isTokenRune(r rune) bool {
	return r < 0x250 && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// tokenize splits s into runs of Latin letters or digits. A letter/digit
// boundary also splits ("HK01" → "HK", "01"; "100GB" → "100", "GB").
func tokenize(s string) []token {
	var toks []token
	start := -1
	digits := false
	flush := func(end int) {
		if start >= 0 {
			toks = append(toks, token{s[start:end], start, end, digits})
			start = -1
		}
	}
	for i, r := range s {
		if !isTokenRune(r) {
			flush(i)
			continue
		}
		d := unicode.IsDigit(r)
		if start >= 0 && d != digits {
			flush(i)
		}
		if start < 0 {
			start = i
			digits = d
		}
	}
	flush(len(s))
	return toks
}

const (
	regionalA = 0x1F1E6
	regionalZ = 0x1F1FF
)

// collect gathers every alias occurrence in name.
func collect(name string) []hit {
	var hits []hit

	// Flag emoji: pairs of regional indicator symbols.
	runes := []rune(name)
	offs := make([]int, 0, len(runes)+1)
	for i := range name {
		offs = append(offs, i)
	}
	offs = append(offs, len(name))
	for i := 0; i+1 < len(runes); i++ {
		a, b := runes[i], runes[i+1]
		if a < regionalA || a > regionalZ || b < regionalA || b > regionalZ {
			continue
		}
		code := string([]byte{byte('A' + a - regionalA), byte('A' + b - regionalA)})
		if idx, ok := countryByISO[code]; ok {
			hits = append(hits, hit{offs[i], offs[i+2], kindCountry, idx, nil})
		}
		i++
	}

	// Chinese (and other non-Latin) aliases: substrings.
	for _, a := range cjkAliases {
		from := 0
		for {
			p := strings.Index(name[from:], a.text)
			if p < 0 {
				break
			}
			s := from + p
			hits = append(hits, hit{s, s + len(a.text), a.kind, a.idx, nil})
			from = s + len(a.text)
		}
	}

	// Latin aliases: whole-token sequences.
	toks := tokenize(name)
	for i, t := range toks {
		if t.digits {
			continue
		}
		cands := tokenAliases[strings.ToLower(t.text)]
		for _, c := range cands {
			if i+len(c.tokens) > len(toks) {
				continue
			}
			ok := true
			for j, want := range c.tokens {
				got := toks[i+j].text
				if c.exact && got != want || !c.exact && !strings.EqualFold(got, want) {
					ok = false
					break
				}
			}
			if !ok || skipToken(name, toks, i, c) {
				continue
			}
			hits = append(hits, hit{t.start, toks[i+len(c.tokens)-1].end, c.kind, c.idx, nil})
		}
	}
	return hits
}

// skipToken filters out matches that are really units or ordinals:
// "100GB" / "500 GB" is a data allowance, not Great Britain; "NO.1" /
// "NO1" is a number, not Norway.
func skipToken(name string, toks []token, i int, c tokenAlias) bool {
	if len(c.tokens) != 1 {
		return false
	}
	switch strings.ToUpper(c.tokens[0]) {
	case "GB":
		return i > 0 && toks[i-1].digits && strings.TrimSpace(name[toks[i-1].end:toks[i].start]) == ""
	case "NO":
		if i+1 < len(toks) && toks[i+1].digits {
			sep := strings.TrimSpace(name[toks[i].end:toks[i+1].start])
			return sep == "" || sep == "."
		}
	}
	return false
}

// resolve picks the winning hit: longer spans suppress contained ones,
// then the highest tier wins, then the earliest position.
func resolve(name string, cityOnly bool) (hit, bool) {
	ensureIndex()
	hits := collect(name)
	if len(hits) == 0 {
		return hit{}, false
	}

	// Merge hits with identical span and kind=city into one with alternatives
	// (shared Chinese alias such as 圣地亚哥).
	merged := hits[:0]
	for _, h := range hits {
		dup := false
		for k := range merged {
			m := &merged[k]
			if m.kind == kindCity && h.kind == kindCity && m.start == h.start && m.end == h.end && m.idx != h.idx {
				m.alts = append(m.alts, h.idx)
				dup = true
				break
			}
			if m.kind == h.kind && m.idx == h.idx && m.start == h.start && m.end == h.end {
				dup = true
				break
			}
		}
		if !dup {
			merged = append(merged, h)
		}
	}
	hits = merged

	// Countries named anywhere, used to disambiguate shared city aliases.
	named := map[string]bool{}
	for _, h := range hits {
		if h.kind == kindCountry {
			named[countries[h.idx].Code] = true
		}
	}

	best := -1
	bestTier := -1
	for i, h := range hits {
		if contained(h, hits) {
			continue
		}
		if cityOnly && h.kind != kindCity {
			continue
		}
		if len(h.alts) > 0 {
			hits[i].idx = pickCity(h, named)
			h = hits[i]
		}
		t := tier(h)
		if t > bestTier || t == bestTier && h.start < hits[best].start {
			best, bestTier = i, t
		}
	}
	if best < 0 {
		return hit{}, false
	}
	return hits[best], true
}

func contained(h hit, all []hit) bool {
	for _, o := range all {
		if o.start <= h.start && h.end <= o.end && o.end-o.start > h.end-h.start {
			return true
		}
	}
	return false
}

func pickCity(h hit, named map[string]bool) int {
	if named[cities[h.idx].Country] {
		return h.idx
	}
	for _, alt := range h.alts {
		if named[cities[alt].Country] {
			return alt
		}
	}
	return h.idx
}

func tier(h hit) int {
	switch h.kind {
	case kindCity:
		if cities[h.idx].Country == "CN" {
			return tierCityCN
		}
		return tierCity
	default:
		if countries[h.idx].Code == "CN" {
			return tierCountryCN
		}
		return tierCountry
	}
}

func cityPlace(i int) model.Place {
	c := cities[i]
	return model.Place{Country: c.Country, City: c.Name, Lat: c.Lat, Lon: c.Lon}
}

// countryOnlyPlace is a country whose city is unknown: the country's
// Chinese name as the display name, at the main city's coordinates.
func countryOnlyPlace(i int) model.Place {
	c := countries[i]
	p, _ := country(c.Code)
	p.City = c.Name
	return p
}

func placeOf(h hit) model.Place {
	if h.kind == kindCity {
		return cityPlace(h.idx)
	}
	return countryOnlyPlace(h.idx)
}

func fromName(name string) (model.Place, bool) {
	h, ok := resolve(name, false)
	if !ok {
		return model.Place{}, false
	}
	return placeOf(h), true
}

func fromTrace(loc, colo string) (model.Place, bool) {
	ensureIndex()
	if i, ok := cityByColo[strings.ToUpper(strings.TrimSpace(colo))]; ok {
		return cityPlace(i), true
	}
	return country(loc)
}

func country(code string) (model.Place, bool) {
	ensureIndex()
	i, ok := countryByISO[strings.ToUpper(strings.TrimSpace(code))]
	if !ok {
		return model.Place{}, false
	}
	if ci, ok := cityByName[countries[i].City]; ok {
		return cityPlace(ci), true
	}
	return model.Place{Country: countries[i].Code, City: countries[i].Name}, true
}

func countryName(code string) string {
	ensureIndex()
	if i, ok := countryByISO[strings.ToUpper(strings.TrimSpace(code))]; ok {
		return countries[i].Name
	}
	return ""
}

func flag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return string([]rune{regionalA + rune(code[0]-'A'), regionalA + rune(code[1]-'A')})
}
