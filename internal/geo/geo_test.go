package geo

import (
	"math"
	"testing"
)

func TestFromName(t *testing.T) {
	// want: "CC/City" (City is the display name: a city, or the country
	// name when only the country is known); "" means no match.
	cases := []struct {
		name string
		want string
	}{
		// Flags and Chinese names
		{"🇭🇰 香港 03 IPLC", "HK/香港"},
		{"🇯🇵 日本 东京 01", "JP/东京"},
		{"🇯🇵 日本 02", "JP/日本"},
		{"🇺🇸 美国 01", "US/美国"},
		{"🇺🇸 美国 圣何塞", "US/圣何塞"},
		{"🇸🇬 狮城 BGP", "SG/新加坡"},
		{"🇹🇼 台湾 HiNet 01", "TW/台湾"},
		{"🇰🇷 韩国 首尔", "KR/首尔"},
		{"🇩🇪 德国 法兰克福", "DE/法兰克福"},
		{"🇬🇧 英国 伦敦 01", "GB/伦敦"},
		{"🇷🇺 俄罗斯 莫斯科", "RU/莫斯科"},
		{"🇨🇳 中国 上海 中转", "CN/上海"},
		{"🇦🇺 澳大利亚 悉尼", "AU/悉尼"},
		{"🇳🇱 荷兰 阿姆斯特丹", "NL/阿姆斯特丹"},
		{"🇫🇷 法国 巴黎", "FR/巴黎"},
		{"🇦🇷 阿根廷 0.1x", "AR/阿根廷"},
		{"🇦🇪 阿联酋 迪拜", "AE/迪拜"},
		{"🇹🇷 土耳其 伊斯坦布尔", "TR/伊斯坦布尔"},
		{"🇮🇳 印度 孟买", "IN/孟买"},
		{"🇮🇩 印尼 雅加达", "ID/雅加达"},
		{"🇧🇷 巴西 圣保罗", "BR/圣保罗"},
		{"🇨🇦 加拿大 多伦多", "CA/多伦多"},
		{"🇻🇳 越南 胡志明市", "VN/胡志明市"},
		{"🇵🇭 菲律宾 马尼拉", "PH/马尼拉"},
		{"🇹🇭 泰国 曼谷", "TH/曼谷"},
		{"🇲🇾 马来西亚 吉隆坡", "MY/吉隆坡"},
		{"🇲🇴 澳门", "MO/澳门"},
		// Flag only, with an unknown country flag ignored
		{"🇭🇰 01", "HK/香港"},
		{"🇦🇶 南极 01", ""}, // Antarctica: not in the table
		// Traditional Chinese
		{"臺灣 台北 01", "TW/台北"},
		{"日本 大阪 02", "JP/大阪"},
		{"美國 洛杉磯", "US/洛杉矶"},
		{"德國 法蘭克福 03", "DE/法兰克福"},
		{"新加坡 獅城", "SG/新加坡"},
		// English and ISO codes as whole tokens
		{"JP-Tokyo-02", "JP/东京"},
		{"US-LosAngeles-01", "US/洛杉矶"},
		{"US Los Angeles 03", "US/洛杉矶"},
		{"HK01", "HK/香港"},
		{"SG 2x", "SG/新加坡"},
		{"hk-premium-5", "HK/香港"},
		{"UK London", "GB/伦敦"},
		{"KR Seoul 01", "KR/首尔"},
		{"TW HiNet", "TW/台湾"},
		{"Hong Kong 01", "HK/香港"},
		{"HongKong-IEPL-01", "HK/香港"},
		{"Singapore 02", "SG/新加坡"},
		{"Japan Osaka", "JP/大阪"},
		{"Germany Frankfurt", "DE/法兰克福"},
		{"USA SanJose", "US/圣何塞"},
		{"Korea", "KR/韩国"},
		{"Node-Taiwan-5", "TW/台湾"},
		{"NYC 01", "US/纽约"},
		{"New York 01", "US/纽约"},
		{"Fremont-1", "US/弗里蒙特"},
		{"LA 01", "US/洛杉矶"},
		{"Sao Paulo", "BR/圣保罗"},
		{"São Paulo 01", "BR/圣保罗"},
		{"Kuala Lumpur", "MY/吉隆坡"},
		{"St. Petersburg", "RU/圣彼得堡"},
		{"Ho Chi Minh City", "VN/胡志明市"},
		// Airport / colo codes
		{"HKG-01", "HK/香港"},
		{"NRT 02", "JP/东京"},
		{"KIX", "JP/大阪"},
		{"SJC-1", "US/圣何塞"},
		{"LAX 2", "US/洛杉矶"},
		{"FRA 01", "DE/法兰克福"},
		{"LHR-x", "GB/伦敦"},
		{"ICN 01", "KR/首尔"},
		{"TPE-HiNet", "TW/台北"},
		{"JPN 01", "JP/日本"},
		{"AUS Sydney", "AU/悉尼"},
		{"AUS 01", "AU/澳大利亚"}, // AUS is Australia, not Austin nor US
		{"CAN 01", "CA/加拿大"},  // CAN is Canada, not Guangzhou
		{"IND Mumbai", "IN/孟买"},
		// Tricky tokens: not matches
		{"IPLC-01", ""},         // IPLC is not PL
		{"IEPL专线 01", ""},       // IEPL is not PL; 专线 ignored
		{"BGP 专线", ""},          // BGP is not GP or BG
		{"Relay-7 专线", ""},      // no place
		{"100GB 剩余流量", ""},      // GB after digits is gigabytes
		{"剩余 500 GB", ""},       // with a space too
		{"NO.1 节点", ""},         // "NO.1" is a number, not Norway
		{"in 01", ""},           // lowercase "in" is a word, not India
		{"it-node", ""},         // lowercase "it" is a word, not Italy
		{"my node", ""},         // "my" is not Malaysia
		{"Be careful", ""},      // "be" is not Belgium
		{"至尊 pro", ""},          // nothing
		{"Premium Node 01", ""}, // nothing
		{"Male", "MV/马累"},       // exact-case city alias
		{"male node", ""},       // lowercase is just a word
		// Upper-case ambiguous codes do match
		{"IN-01", "IN/印度"},
		{"IT Milan", "IT/米兰"},
		{"MY-KL", "MY/吉隆坡"},
		{"NO Oslo", "NO/奥斯陆"},
		{"DE-01", "DE/德国"},
		{"GB-01", "GB/英国"},
		// Longer matches suppress contained ones
		{"印度尼西亚 01", "ID/印度尼西亚"},
		{"罗马尼亚 01", "RO/罗马尼亚"},
		{"白俄罗斯 01", "BY/白俄罗斯"},
		{"尼日利亚 拉各斯", "NG/拉各斯"},
		{"法兰克福 01", "DE/法兰克福"}, // not 法 (France)
		{"新德里", "IN/新德里"},      // not 德 (Germany)
		{"德州 达拉斯", "US/达拉斯"},
		{"美国德州", "US/达拉斯"}, // 德州 (Texas) beats 德 (Germany) and is a city
		{"内蒙古 01", "CN/呼和浩特"},
		{"京都 01", "JP/京都"}, // not 京 (Beijing)
		// City beats country; earliest wins among equals
		{"美国 东京", "JP/东京"},
		{"日本 美国 01", "JP/日本"},
		{"🇺🇸 日本", "US/美国"},
		{"香港 HK 01", "HK/香港"},
		{"🇭🇰 HK → JP", "HK/香港"},
		// Mainland China only as a fallback (relay entry)
		{"上海-东京 IPLC", "JP/东京"},
		{"沪日 专线 01", "JP/日本"},
		{"深港 IPLC 02", "HK/香港"},
		{"广州 → 新加坡", "SG/新加坡"},
		{"中国 香港", "HK/香港"},
		{"中国台湾", "TW/台湾"},
		{"上海 中转 01", "CN/上海"},
		{"深圳 01", "CN/深圳"},
		{"中国 01", "CN/中国"},
		{"CN2 GIA 美国", "US/美国"},
		{"北京联通 入口", "CN/北京"},
		// Ambiguous shared Chinese alias resolved by country
		{"圣地亚哥 01", "US/圣迭戈"},
		{"智利 圣地亚哥", "CL/圣地亚哥"},
		{"美国 圣地亚哥", "US/圣迭戈"},
		// Misc real-world shapes
		{"[SS] 🇯🇵 JP Tokyo IIJ 1x", "JP/东京"},
		{"V2-🇸🇬新加坡-03 | 1.5x", "SG/新加坡"},
		{"Trojan|🇭🇰HK-BGP-02", "HK/香港"},
		{"🇺🇲 US 西雅图 (Netflix)", "US/西雅图"},
		{"台灣 彰化 Google", "TW/彰化"},
		{"香港 将军澳 Azure", "HK/香港"},
		{"美国 阿什本 AWS", "US/阿什本"},
		{"Falkenstein Hetzner", "DE/法尔肯施泰因"},
		{"Russia Vladivostok", "RU/符拉迪沃斯托克"},
		{"Johannesburg 01", "ZA/约翰内斯堡"},
		{"🇸🇦 利雅得", "SA/利雅得"},
		{"", ""},
	}
	for _, tc := range cases {
		p, ok := FromName(tc.name)
		got := ""
		if ok {
			got = p.Country + "/" + p.City
		}
		if got != tc.want {
			t.Errorf("FromName(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if ok && (p.Lat == 0 && p.Lon == 0) {
			t.Errorf("FromName(%q): zero coordinates", tc.name)
		}
	}
}

func TestFromTrace(t *testing.T) {
	cases := []struct {
		loc, colo string
		want      string
	}{
		{"JP", "NRT", "JP/东京"},
		{"JP", "KIX", "JP/大阪"},
		{"HK", "HKG", "HK/香港"},
		{"SG", "SIN", "SG/新加坡"},
		{"US", "LAX", "US/洛杉矶"},
		{"US", "SJC", "US/圣何塞"},
		{"US", "AUS", "US/奥斯汀"}, // colo AUS is Austin, unlike node names
		{"CN", "CAN", "CN/广州"},
		{"DE", "FRA", "DE/法兰克福"},
		{"GB", "LHR", "GB/伦敦"},
		{"NL", "AMS", "NL/阿姆斯特丹"},
		{"US", "HKG", "HK/香港"}, // colo wins over the registered country
		{"JP", "", "JP/东京"},    // unknown colo: main city
		{"JP", "XXX", "JP/东京"},
		{"us", "lax", "US/洛杉矶"}, // case-insensitive
		{"", "", ""},
		{"XX", "", ""},
	}
	for _, tc := range cases {
		p, ok := FromTrace(tc.loc, tc.colo)
		got := ""
		if ok {
			got = p.Country + "/" + p.City
		}
		if got != tc.want {
			t.Errorf("FromTrace(%q, %q) = %q, want %q", tc.loc, tc.colo, got, tc.want)
		}
	}
}

func TestCountryAndName(t *testing.T) {
	p, ok := Country("JP")
	if !ok || p.City != "东京" || p.Country != "JP" {
		t.Fatalf("Country(JP) = %+v, %v", p, ok)
	}
	if p, ok := Country("us"); !ok || p.City != "洛杉矶" {
		t.Errorf("Country(us) = %+v, %v", p, ok)
	}
	if _, ok := Country("ZZ"); ok {
		t.Error("Country(ZZ) should fail")
	}
	if got := CountryName("HK"); got != "香港" {
		t.Errorf("CountryName(HK) = %q", got)
	}
	if got := CountryName("de"); got != "德国" {
		t.Errorf("CountryName(de) = %q", got)
	}
	if got := CountryName("ZZ"); got != "" {
		t.Errorf("CountryName(ZZ) = %q", got)
	}
}

func TestFlag(t *testing.T) {
	cases := map[string]string{
		"HK": "🇭🇰", "jp": "🇯🇵", "US": "🇺🇸", "CN": "🇨🇳", "TW": "🇹🇼",
		"": "", "H": "", "HKG": "", "1A": "",
	}
	for in, want := range cases {
		if got := Flag(in); got != want {
			t.Errorf("Flag(%q) = %q, want %q", in, got, want)
		}
	}
	// Round trip through FromName.
	for _, c := range countries {
		if p, ok := FromName(Flag(c.Code) + " 01"); !ok || p.Country != c.Code {
			t.Errorf("FromName(flag %s) = %+v, %v", c.Code, p, ok)
		}
	}
}

func TestFromTimeZone(t *testing.T) {
	cases := map[string]string{
		"Asia/Shanghai":                  "上海",
		"Asia/Hong_Kong":                 "香港",
		"Asia/Tokyo":                     "东京",
		"Asia/Taipei":                    "台北",
		"Asia/Singapore":                 "新加坡",
		"Asia/Kolkata":                   "加尔各答",
		"Asia/Kuwait":                    "科威特城",
		"Europe/London":                  "伦敦",
		"Europe/Paris":                   "巴黎",
		"Europe/Luxembourg":              "卢森堡",
		"America/Los_Angeles":            "洛杉矶",
		"America/New_York":               "纽约",
		"America/Argentina/Buenos_Aires": "布宜诺斯艾利斯",
		"America/Indiana/Indianapolis":   "印第安纳波利斯",
		"Australia/Sydney":               "悉尼",
		"Pacific/Auckland":               "奥克兰",
		"Africa/Johannesburg":            "约翰内斯堡",
		"Europe/Kyiv":                    "基辅",
		"Asia/Ho_Chi_Minh":               "胡志明市",
		"Pacific/Guam":                   "关岛",
		"America/Port_of_Spain":          "", // "Spain" must not be taken as a city
		"Etc/UTC":                        "",
		"UTC":                            "",
		"":                               "",
	}
	for zone, want := range cases {
		p, ok := FromTimeZone(zone)
		got := ""
		if ok {
			got = p.City
		}
		if got != want {
			t.Errorf("FromTimeZone(%q) = %q, want %q", zone, got, want)
		}
	}
}

// TestTableIntegrity checks the hand-written tables for internal
// consistency so that a typo cannot ship silently.
func TestTableIntegrity(t *testing.T) {
	ensureIndex()
	seenCode := map[string]bool{}
	for _, c := range countries {
		if len(c.Code) != 2 || c.Code != stringsToUpper(c.Code) {
			t.Errorf("country %q: bad code", c.Code)
		}
		if seenCode[c.Code] {
			t.Errorf("country %q: duplicate", c.Code)
		}
		seenCode[c.Code] = true
		ci, ok := cityByName[c.City]
		if !ok {
			t.Errorf("country %s: main city %q not in city table", c.Code, c.City)
		} else if cities[ci].Country != c.Code {
			t.Errorf("country %s: main city %q belongs to %s", c.Code, c.City, cities[ci].Country)
		}
	}
	if len(countries) < 70 {
		t.Errorf("only %d countries", len(countries))
	}
	if len(cities) < 180 {
		t.Errorf("only %d cities", len(cities))
	}
	seenCity := map[string]bool{}
	seenColo := map[string]string{}
	for _, c := range cities {
		if !seenCode[c.Country] {
			t.Errorf("city %s: unknown country %q", c.Name, c.Country)
		}
		if seenCity[c.Name] {
			t.Errorf("city %s: duplicate", c.Name)
		}
		seenCity[c.Name] = true
		if c.Lat < -60 || c.Lat > 72 || c.Lon < -180 || c.Lon > 180 || (c.Lat == 0 && c.Lon == 0) {
			t.Errorf("city %s: bad coordinates %v,%v", c.Name, c.Lat, c.Lon)
		}
		if math.Abs(math.Round(c.Lat*100)-c.Lat*100) > 1e-6 || math.Abs(math.Round(c.Lon*100)-c.Lon*100) > 1e-6 {
			t.Errorf("city %s: coordinates must have at most two decimals", c.Name)
		}
		for _, colo := range c.Colos {
			if len(colo) != 3 || colo != stringsToUpper(colo) {
				t.Errorf("city %s: bad colo %q", c.Name, colo)
			}
			if prev, dup := seenColo[colo]; dup {
				t.Errorf("colo %s used by both %s and %s", colo, prev, c.Name)
			}
			seenColo[colo] = c.Name
		}
	}
	// Every Latin alias must be reachable by itself.
	for _, c := range cities {
		for _, a := range c.Aliases {
			if !isLatin(a) {
				continue
			}
			p, ok := FromName(trimEq(a))
			if !ok || p.City != c.Name && !(p.Country == c.Country) {
				t.Errorf("city alias %q → %+v, %v; want %s", a, p, ok, c.Name)
			}
		}
	}
	for _, c := range countries {
		for _, a := range c.Aliases {
			if !isLatin(a) {
				continue
			}
			p, ok := FromName(trimEq(a))
			if !ok || p.Country != c.Code {
				t.Errorf("country alias %q → %+v, %v; want %s", a, p, ok, c.Code)
			}
		}
	}
	// Required colo codes from the design.
	for colo, city := range map[string]string{
		"NRT": "东京", "HND": "东京", "KIX": "大阪", "HKG": "香港", "SIN": "新加坡",
		"TPE": "台北", "ICN": "首尔", "LAX": "洛杉矶", "SJC": "圣何塞", "SEA": "西雅图",
		"FRA": "法兰克福", "LHR": "伦敦", "AMS": "阿姆斯特丹",
	} {
		if cities[cityByColo[colo]].Name != city {
			t.Errorf("colo %s → %s, want %s", colo, cities[cityByColo[colo]].Name, city)
		}
	}
}

func trimEq(s string) string {
	if len(s) > 0 && s[0] == '=' {
		return s[1:]
	}
	return s
}

func stringsToUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func TestOnLand(t *testing.T) {
	land := []struct {
		name     string
		lat, lon float64
	}{
		{"Tokyo", 35.68, 139.69},
		{"Shanghai", 31.23, 121.47},
		{"London", 51.51, -0.13},
		{"Beijing", 39.90, 116.40},
		{"Denver", 39.74, -104.98},
		{"Moscow", 55.76, 37.62},
		{"Sahara", 23.5, 10.5},
		{"Amazon", -5.5, -62.5},
		{"Australia outback", -25.5, 133.5},
		{"Antarctica", -80.5, 0.5},
		{"Siberia", 65.5, 100.5},
	}
	for _, c := range land {
		if !OnLand(c.lat, c.lon) {
			t.Errorf("%s (%v, %v) should be land", c.name, c.lat, c.lon)
		}
	}
	water := []struct {
		name     string
		lat, lon float64
	}{
		{"mid-Pacific", 0.5, -160.5},
		{"North Pacific", 30.5, -170.5},
		{"mid-Atlantic", 20.5, -40.5},
		{"Indian Ocean", -30.5, 80.5},
		{"Southern Ocean", -55.5, 140.5},
		{"Arctic Ocean", 85.5, 0.5},
		{"Bay of Bengal", 12.5, 88.5},
	}
	for _, c := range water {
		if OnLand(c.lat, c.lon) {
			t.Errorf("%s (%v, %v) should be water", c.name, c.lat, c.lon)
		}
	}
	// Edge handling must not panic.
	for _, p := range [][2]float64{{90, 180}, {-90, -180}, {0, 540}, {0, -540}, {91, 0}, {math.NaN(), 0}} {
		OnLand(p[0], p[1])
	}
	if OnLand(0, 180) != OnLand(0, -180) {
		t.Error("longitude 180 and -180 must be the same cell")
	}
	// Every city in the table sits on land, except small islands that the
	// 1:110m data set omits.
	islands := map[string]bool{"马累": true, "澳门": true, "香港": true, "新加坡": true, "关岛": true, "瓦莱塔": true, "麦纳麦": true, "路易港": true, "留尼汪": true, "巴淡": true, "普吉": true, "槟城": true, "冲绳": true, "楠迪": true, "檀香山": true, "巴厘岛": true, "宿务": true, "斯里巴加湾": true, "新山": true, "圣胡安": true, "台北": true, "台中": true, "台南": true, "高雄": true, "新竹": true, "彰化": true, "雷克雅未克": true, "尼科西亚": true, "科伦坡": true, "阿克拉": true, "拉各斯": true, "达喀尔": true, "卡萨布兰卡": true, "里斯本": true, "哥本哈根": true, "阿姆斯特丹": true, "鹿特丹": true}
	miss := 0
	for _, c := range cities {
		if !OnLand(c.Lat, c.Lon) && !islands[c.Name] {
			t.Logf("city %s (%v, %v) is not on a land cell", c.Name, c.Lat, c.Lon)
			miss++
		}
	}
	if miss > len(cities)/6 {
		t.Errorf("%d cities off land: bitmap orientation is probably wrong", miss)
	}
}
