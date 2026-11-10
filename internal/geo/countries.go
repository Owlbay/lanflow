package geo

// countryEntry is one row of the country table.
//
// Aliases are matched in node names: entries containing non-Latin
// characters (Chinese) match as substrings; Latin entries match whole
// tokens case-insensitively. A leading "=" makes a Latin alias
// case-sensitive, used for codes that are also English words ("IN", "IT",
// "NO", ...) so that they only count when written in upper case.
//
// The ISO code itself is always an alias; two-letter codes listed in
// caseSensitiveCodes need upper case.
type countryEntry struct {
	Code    string // ISO 3166-1 alpha-2
	Name    string // Chinese (simplified) display name
	City    string // main city, must exist in the city table
	Aliases []string
}

// caseSensitiveCodes are ISO codes that are also everyday English words
// (or common lowercase abbreviations) and therefore only match in upper case.
var caseSensitiveCodes = map[string]bool{
	"IN": true, "IT": true, "MY": true, "NO": true, "AT": true, "BE": true,
	"CO": true, "DE": true, "ID": true, "IS": true, "AM": true, "BY": true,
	"MO": true, "PA": true, "MA": true, "CD": true, "RE": true, "SI": true,
	"GE": true, "SE": true, "ES": true,
}

// countries is written from public knowledge (ISO 3166, common Chinese
// usage in proxy node names). Order matters only for "earliest wins" ties
// between identical spans, which cannot happen across countries.
var countries = []countryEntry{
	// --- East Asia ---
	{"HK", "香港", "香港", []string{"港", "HKG", "Hong Kong", "HongKong", "Hongkong"}},
	{"MO", "澳门", "澳门", []string{"澳門", "=MAC", "Macau", "Macao"}},
	{"TW", "台湾", "台北", []string{"台灣", "臺灣", "台", "TWN", "Taiwan"}},
	{"JP", "日本", "东京", []string{"日", "JPN", "Japan"}},
	{"KR", "韩国", "首尔", []string{"韓國", "南韩", "南韓", "韩", "KOR", "Korea", "South Korea", "SouthKorea"}},
	{"CN", "中国", "上海", []string{"中國", "大陆", "大陸", "内地", "內地", "国内", "國內", "CHN", "China", "Mainland", "PRC"}},
	{"MN", "蒙古", "乌兰巴托", []string{"MNG", "Mongolia"}},

	// --- Southeast Asia ---
	{"SG", "新加坡", "新加坡", []string{"SGP", "Singapore"}},
	{"MY", "马来西亚", "吉隆坡", []string{"馬來西亞", "马来", "馬來", "大马", "大馬", "MYS", "Malaysia"}},
	{"TH", "泰国", "曼谷", []string{"泰國", "泰", "THA", "Thailand"}},
	{"ID", "印度尼西亚", "雅加达", []string{"印度尼西亞", "印尼", "IDN", "Indonesia"}},
	{"PH", "菲律宾", "马尼拉", []string{"菲律賓", "菲", "PHL", "Philippines"}},
	{"VN", "越南", "胡志明市", []string{"VNM", "Vietnam", "Viet Nam"}},
	{"KH", "柬埔寨", "金边", []string{"柬", "KHM", "Cambodia"}},
	{"LA", "老挝", "万象", []string{"老撾", "寮国", "寮國", "LAO", "Laos"}},
	{"MM", "缅甸", "仰光", []string{"緬甸", "缅", "MMR", "Myanmar", "Burma"}},
	{"BN", "文莱", "斯里巴加湾", []string{"汶莱", "汶萊", "BRN", "Brunei"}},

	// --- South Asia ---
	{"IN", "印度", "孟买", []string{"印", "=IND", "India"}},
	{"PK", "巴基斯坦", "卡拉奇", []string{"PAK", "Pakistan"}},
	{"BD", "孟加拉国", "达卡", []string{"孟加拉", "BGD", "Bangladesh"}},
	{"LK", "斯里兰卡", "科伦坡", []string{"斯里蘭卡", "LKA", "Sri Lanka", "SriLanka"}},
	{"NP", "尼泊尔", "加德满都", []string{"尼泊爾", "NPL", "Nepal"}},
	{"MV", "马尔代夫", "马累", []string{"馬爾代夫", "马代", "MDV", "Maldives"}},

	// --- Central Asia / Middle East ---
	{"KZ", "哈萨克斯坦", "阿拉木图", []string{"哈薩克", "哈萨克", "KAZ", "Kazakhstan"}},
	{"UZ", "乌兹别克斯坦", "塔什干", []string{"烏茲別克", "乌兹别克", "UZB", "Uzbekistan"}},
	{"AE", "阿联酋", "迪拜", []string{"阿聯酋", "阿拉伯联合酋长国", "=ARE", "UAE", "Emirates", "United Arab Emirates"}},
	{"QA", "卡塔尔", "多哈", []string{"卡塔爾", "卡达", "卡達", "QAT", "Qatar"}},
	{"SA", "沙特阿拉伯", "利雅得", []string{"沙特", "沙乌地", "沙烏地", "SAU", "Saudi", "Saudi Arabia"}},
	{"KW", "科威特", "科威特城", []string{"KWT", "Kuwait"}},
	{"BH", "巴林", "麦纳麦", []string{"BHR", "Bahrain"}},
	{"OM", "阿曼", "马斯喀特", []string{"OMN", "Oman"}},
	{"JO", "约旦", "安曼", []string{"約旦", "JOR", "Jordan"}},
	{"IL", "以色列", "特拉维夫", []string{"ISR", "Israel"}},
	{"IQ", "伊拉克", "巴格达", []string{"IRQ", "Iraq"}},
	{"IR", "伊朗", "德黑兰", []string{"IRN", "Iran"}},
	{"TR", "土耳其", "伊斯坦布尔", []string{"TUR", "Turkey", "Turkiye", "Türkiye"}},
	{"GE", "格鲁吉亚", "第比利斯", []string{"格魯吉亞", "乔治亚", "喬治亞", "GEO", "Georgia"}},
	{"AZ", "阿塞拜疆", "巴库", []string{"亞塞拜然", "AZE", "Azerbaijan"}},
	{"AM", "亚美尼亚", "埃里温", []string{"亞美尼亞", "=ARM", "Armenia"}},
	{"LB", "黎巴嫩", "贝鲁特", []string{"LBN", "Lebanon"}},

	// --- Oceania ---
	{"AU", "澳大利亚", "悉尼", []string{"澳大利亞", "澳洲", "AUS", "Australia"}},
	{"NZ", "新西兰", "奥克兰", []string{"新西蘭", "纽西兰", "紐西蘭", "NZL", "New Zealand", "NewZealand"}},
	{"GU", "关岛", "关岛", []string{"關島", "Guam"}},
	{"FJ", "斐济", "楠迪", []string{"斐濟", "FJI", "Fiji"}},

	// --- North America ---
	{"US", "美国", "洛杉矶", []string{"美國", "美", "USA", "United States", "UnitedStates", "America"}},
	{"CA", "加拿大", "多伦多", []string{"枫叶国", "楓葉國", "=CAN", "Canada"}},
	{"MX", "墨西哥", "墨西哥城", []string{"墨", "Mexico"}},
	{"PA", "巴拿马", "巴拿马城", []string{"巴拿馬", "=PAN", "Panama"}},
	{"PR", "波多黎各", "圣胡安", []string{"PRI", "Puerto Rico", "PuertoRico"}},

	// --- South America ---
	{"BR", "巴西", "圣保罗", []string{"=BRA", "Brazil", "Brasil"}},
	{"AR", "阿根廷", "布宜诺斯艾利斯", []string{"ARG", "Argentina"}},
	{"CL", "智利", "圣地亚哥", []string{"CHL", "Chile"}},
	{"PE", "秘鲁", "利马", []string{"秘魯", "=PER", "Peru"}},
	{"CO", "哥伦比亚", "波哥大", []string{"哥倫比亞", "=COL", "Colombia"}},
	{"EC", "厄瓜多尔", "基多", []string{"厄瓜多爾", "ECU", "Ecuador"}},
	{"UY", "乌拉圭", "蒙得维的亚", []string{"烏拉圭", "URY", "Uruguay"}},

	// --- Europe ---
	{"GB", "英国", "伦敦", []string{"英國", "英", "大不列颠", "UK", "GBR", "United Kingdom", "UnitedKingdom", "Britain", "Great Britain", "England"}},
	{"IE", "爱尔兰", "都柏林", []string{"愛爾蘭", "IRL", "Ireland"}},
	{"FR", "法国", "巴黎", []string{"法國", "法", "France"}},
	{"DE", "德国", "法兰克福", []string{"德國", "德", "GER", "DEU", "Germany", "Deutschland"}},
	{"NL", "荷兰", "阿姆斯特丹", []string{"荷蘭", "荷", "尼德兰", "尼德蘭", "NLD", "Netherlands", "Holland"}},
	{"BE", "比利时", "布鲁塞尔", []string{"比利時", "BEL", "Belgium"}},
	{"LU", "卢森堡", "卢森堡", []string{"盧森堡", "Luxembourg"}},
	{"CH", "瑞士", "苏黎世", []string{"CHE", "Switzerland", "Swiss"}},
	{"AT", "奥地利", "维也纳", []string{"奧地利", "AUT", "Austria"}},
	{"IT", "意大利", "米兰", []string{"義大利", "ITA", "Italy", "Italia"}},
	{"ES", "西班牙", "马德里", []string{"ESP", "Spain", "España", "Espana"}},
	{"PT", "葡萄牙", "里斯本", []string{"葡", "PRT", "Portugal"}},
	{"SE", "瑞典", "斯德哥尔摩", []string{"SWE", "Sweden"}},
	{"NO", "挪威", "奥斯陆", []string{"挪", "=NOR", "Norway"}},
	{"DK", "丹麦", "哥本哈根", []string{"丹麥", "DNK", "Denmark"}},
	{"FI", "芬兰", "赫尔辛基", []string{"芬蘭", "芬", "=FIN", "Finland"}},
	{"PL", "波兰", "华沙", []string{"波蘭", "=POL", "Poland"}},
	{"CZ", "捷克", "布拉格", []string{"捷", "CZE", "Czech", "Czechia"}},
	{"HU", "匈牙利", "布达佩斯", []string{"匈", "HUN", "Hungary"}},
	{"RO", "罗马尼亚", "布加勒斯特", []string{"羅馬尼亞", "ROU", "Romania"}},
	{"BG", "保加利亚", "索非亚", []string{"保加利亞", "BGR", "Bulgaria"}},
	{"GR", "希腊", "雅典", []string{"希臘", "GRC", "Greece"}},
	{"RS", "塞尔维亚", "贝尔格莱德", []string{"塞爾維亞", "SRB", "Serbia"}},
	{"HR", "克罗地亚", "萨格勒布", []string{"克羅地亞", "克罗埃西亚", "HRV", "Croatia"}},
	{"UA", "乌克兰", "基辅", []string{"烏克蘭", "UKR", "Ukraine"}},
	{"RU", "俄罗斯", "莫斯科", []string{"俄羅斯", "俄国", "俄國", "俄", "RUS", "Russia"}},
	{"EE", "爱沙尼亚", "塔林", []string{"愛沙尼亞", "=EST", "Estonia"}},
	{"LV", "拉脱维亚", "里加", []string{"拉脫維亞", "LVA", "Latvia"}},
	{"LT", "立陶宛", "维尔纽斯", []string{"LTU", "Lithuania"}},
	{"BY", "白俄罗斯", "明斯克", []string{"白俄羅斯", "白俄", "Belarus"}},
	{"MD", "摩尔多瓦", "基希讷乌", []string{"摩爾多瓦", "MDA", "Moldova"}},
	{"IS", "冰岛", "雷克雅未克", []string{"冰島", "ISL", "Iceland"}},
	{"MT", "马耳他", "瓦莱塔", []string{"馬耳他", "马尔他", "MLT", "Malta"}},
	{"CY", "塞浦路斯", "尼科西亚", []string{"賽普勒斯", "塞浦路斯", "CYP", "Cyprus"}},
	{"SK", "斯洛伐克", "布拉迪斯拉发", []string{"SVK", "Slovakia"}},
	{"SI", "斯洛文尼亚", "卢布尔雅那", []string{"斯洛維尼亞", "SVN", "Slovenia"}},

	// --- Africa ---
	{"ZA", "南非", "约翰内斯堡", []string{"ZAF", "South Africa", "SouthAfrica"}},
	{"NG", "尼日利亚", "拉各斯", []string{"奈及利亞", "NGA", "Nigeria"}},
	{"KE", "肯尼亚", "内罗毕", []string{"肯亞", "KEN", "Kenya"}},
	{"EG", "埃及", "开罗", []string{"EGY", "Egypt"}},
	{"MA", "摩洛哥", "卡萨布兰卡", []string{"=MAR", "Morocco"}},
	{"TN", "突尼斯", "突尼斯", []string{"突尼西亚", "突尼西亞", "Tunisia"}},
	{"DZ", "阿尔及利亚", "阿尔及尔", []string{"阿爾及利亞", "DZA", "Algeria"}},
	{"GH", "加纳", "阿克拉", []string{"迦納", "GHA", "Ghana"}},
	{"TZ", "坦桑尼亚", "达累斯萨拉姆", []string{"坦尚尼亞", "TZA", "Tanzania"}},
	{"MU", "毛里求斯", "路易港", []string{"模里西斯", "MUS", "Mauritius"}},
	{"CD", "刚果金", "金沙萨", []string{"刚果", "剛果", "=COD", "Congo"}},
	{"AO", "安哥拉", "卢安达", []string{"=AGO", "Angola"}},
	{"ET", "埃塞俄比亚", "亚的斯亚贝巴", []string{"衣索比亞", "ETH", "Ethiopia"}},
	{"ZW", "津巴布韦", "哈拉雷", []string{"辛巴威", "ZWE", "Zimbabwe"}},
	{"UG", "乌干达", "坎帕拉", []string{"烏干達", "UGA", "Uganda"}},
	{"RW", "卢旺达", "基加利", []string{"盧安達", "RWA", "Rwanda"}},
	{"SN", "塞内加尔", "达喀尔", []string{"塞內加爾", "=SEN", "Senegal"}},
	{"RE", "留尼汪", "留尼汪", []string{"留尼旺", "REU", "Reunion", "Réunion"}},
}
