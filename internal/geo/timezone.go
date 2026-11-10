package geo

import (
	"strings"

	"lanflow/internal/model"
)

// zoneCities maps IANA zones whose last path segment is not itself a city
// alias (or is ambiguous) to a city in the table. Zones not listed here
// fall back to matching the last segment against city aliases
// ("Europe/Paris" → 巴黎, "America/Argentina/Buenos_Aires" → 布宜诺斯艾利斯).
var zoneCities = map[string]string{
	"Asia/Shanghai":        "上海",
	"Asia/Chongqing":       "重庆",
	"Asia/Chungking":       "重庆",
	"Asia/Harbin":          "哈尔滨",
	"Asia/Urumqi":          "乌鲁木齐",
	"Asia/Kashgar":         "乌鲁木齐",
	"PRC":                  "上海",
	"Asia/Hong_Kong":       "香港",
	"Hongkong":             "香港",
	"Asia/Macau":           "澳门",
	"Asia/Macao":           "澳门",
	"Asia/Taipei":          "台北",
	"ROC":                  "台北",
	"Asia/Tokyo":           "东京",
	"Japan":                "东京",
	"Asia/Seoul":           "首尔",
	"ROK":                  "首尔",
	"Asia/Singapore":       "新加坡",
	"Singapore":            "新加坡",
	"Asia/Kuala_Lumpur":    "吉隆坡",
	"Asia/Bangkok":         "曼谷",
	"Asia/Jakarta":         "雅加达",
	"Asia/Manila":          "马尼拉",
	"Asia/Ho_Chi_Minh":     "胡志明市",
	"Asia/Saigon":          "胡志明市",
	"Asia/Phnom_Penh":      "金边",
	"Asia/Vientiane":       "万象",
	"Asia/Yangon":          "仰光",
	"Asia/Rangoon":         "仰光",
	"Asia/Brunei":          "斯里巴加湾",
	"Asia/Kolkata":         "加尔各答",
	"Asia/Calcutta":        "加尔各答",
	"Asia/Karachi":         "卡拉奇",
	"Asia/Dhaka":           "达卡",
	"Asia/Dacca":           "达卡",
	"Asia/Colombo":         "科伦坡",
	"Asia/Kathmandu":       "加德满都",
	"Asia/Katmandu":        "加德满都",
	"Indian/Maldives":      "马累",
	"Asia/Almaty":          "阿拉木图",
	"Asia/Tashkent":        "塔什干",
	"Asia/Dubai":           "迪拜",
	"Asia/Qatar":           "多哈",
	"Asia/Riyadh":          "利雅得",
	"Asia/Kuwait":          "科威特城",
	"Asia/Bahrain":         "麦纳麦",
	"Asia/Muscat":          "马斯喀特",
	"Asia/Amman":           "安曼",
	"Asia/Jerusalem":       "特拉维夫",
	"Asia/Tel_Aviv":        "特拉维夫",
	"Israel":               "特拉维夫",
	"Asia/Baghdad":         "巴格达",
	"Asia/Tehran":          "德黑兰",
	"Iran":                 "德黑兰",
	"Europe/Istanbul":      "伊斯坦布尔",
	"Asia/Istanbul":        "伊斯坦布尔",
	"Turkey":               "伊斯坦布尔",
	"Asia/Ulaanbaatar":     "乌兰巴托",
	"Asia/Ulan_Bator":      "乌兰巴托",
	"Europe/London":        "伦敦",
	"GB":                   "伦敦",
	"Europe/Dublin":        "都柏林",
	"Eire":                 "都柏林",
	"Europe/Paris":         "巴黎",
	"Europe/Berlin":        "柏林",
	"Europe/Amsterdam":     "阿姆斯特丹",
	"Europe/Brussels":      "布鲁塞尔",
	"Europe/Luxembourg":    "卢森堡",
	"Europe/Zurich":        "苏黎世",
	"Europe/Vienna":        "维也纳",
	"Europe/Rome":          "罗马",
	"Europe/Madrid":        "马德里",
	"Europe/Lisbon":        "里斯本",
	"Portugal":             "里斯本",
	"Europe/Stockholm":     "斯德哥尔摩",
	"Europe/Oslo":          "奥斯陆",
	"Europe/Copenhagen":    "哥本哈根",
	"Europe/Helsinki":      "赫尔辛基",
	"Europe/Warsaw":        "华沙",
	"Poland":               "华沙",
	"Europe/Prague":        "布拉格",
	"Europe/Budapest":      "布达佩斯",
	"Europe/Bucharest":     "布加勒斯特",
	"Europe/Sofia":         "索非亚",
	"Europe/Athens":        "雅典",
	"Europe/Belgrade":      "贝尔格莱德",
	"Europe/Zagreb":        "萨格勒布",
	"Europe/Kyiv":          "基辅",
	"Europe/Kiev":          "基辅",
	"Europe/Moscow":        "莫斯科",
	"W-SU":                 "莫斯科",
	"Europe/Malta":         "瓦莱塔",
	"Asia/Nicosia":         "尼科西亚",
	"Europe/Nicosia":       "尼科西亚",
	"Atlantic/Reykjavik":   "雷克雅未克",
	"Iceland":              "雷克雅未克",
	"Australia/Sydney":     "悉尼",
	"Australia/NSW":        "悉尼",
	"Australia/ACT":        "堪培拉",
	"Australia/Canberra":   "堪培拉",
	"Australia/Melbourne":  "墨尔本",
	"Australia/Victoria":   "墨尔本",
	"Australia/Brisbane":   "布里斯班",
	"Australia/Queensland": "布里斯班",
	"Australia/Perth":      "珀斯",
	"Australia/West":       "珀斯",
	"Australia/Adelaide":   "阿德莱德",
	"Australia/South":      "阿德莱德",
	"Pacific/Auckland":     "奥克兰",
	"NZ":                   "奥克兰",
	"Pacific/Guam":         "关岛",
	"Pacific/Fiji":         "楠迪",
	"Pacific/Honolulu":     "檀香山",
	"US/Hawaii":            "檀香山",
	"US/Pacific":           "洛杉矶",
	"America/Los_Angeles":  "洛杉矶",
	"US/Mountain":          "丹佛",
	"US/Arizona":           "凤凰城",
	"US/Central":           "芝加哥",
	"US/Eastern":           "纽约",
	"America/New_York":     "纽约",
	"America/Toronto":      "多伦多",
	"America/Montreal":     "蒙特利尔",
	"America/Edmonton":     "卡尔加里",
	"America/Vancouver":    "温哥华",
	"America/Mexico_City":  "墨西哥城",
	"America/Panama":       "巴拿马城",
	"America/Puerto_Rico":  "圣胡安",
	"America/Sao_Paulo":    "圣保罗",
	"America/Santiago":     "圣地亚哥",
	"America/Guayaquil":    "基多",
	"Africa/Johannesburg":  "约翰内斯堡",
	"Africa/Lagos":         "拉各斯",
	"Africa/Nairobi":       "内罗毕",
	"Africa/Cairo":         "开罗",
	"Egypt":                "开罗",
	"Africa/Casablanca":    "卡萨布兰卡",
	"Africa/Tunis":         "突尼斯",
	"Africa/Algiers":       "阿尔及尔",
	"Africa/Accra":         "阿克拉",
	"Indian/Mauritius":     "路易港",
	"Indian/Reunion":       "留尼汪",
	"Africa/Kinshasa":      "金沙萨",
	"Africa/Luanda":        "卢安达",
	"Africa/Addis_Ababa":   "亚的斯亚贝巴",
	"Africa/Harare":        "哈拉雷",
	"Africa/Kampala":       "坎帕拉",
	"Africa/Kigali":        "基加利",
	"Africa/Dakar":         "达喀尔",
}

func fromTimeZone(zone string) (model.Place, bool) {
	ensureIndex()
	zone = strings.TrimSpace(zone)
	// A Windows key name ("China Standard Time") is mapped to its IANA
	// zone first.
	if z, ok := FromWindowsZone(zone); ok {
		zone = z
	}
	if name, ok := zoneCities[zone]; ok {
		if i, ok := cityByName[name]; ok {
			return cityPlace(i), true
		}
	}
	// Fall back to the last path segment as an English city name.
	seg := zone
	if i := strings.LastIndexByte(zone, '/'); i >= 0 {
		seg = zone[i+1:]
	}
	seg = strings.ReplaceAll(seg, "_", " ")
	if seg == "" {
		return model.Place{}, false
	}
	h, ok := resolve(seg, true)
	if !ok {
		return model.Place{}, false
	}
	return cityPlace(h.idx), true
}
