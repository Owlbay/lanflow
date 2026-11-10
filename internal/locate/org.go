package locate

import "strings"

// orgAlias maps a case-insensitive substring of an ASN organisation name to
// the short name shown in the UI. Earlier rows win.
var orgAlias = []struct{ match, short string }{
	{"chinanet", "中国电信"},
	{"china telecom", "中国电信"},
	{"jin-rong street", "中国电信"},
	{"china unicom", "中国联通"},
	{"cncgroup", "中国联通"},
	{"china169", "中国联通"},
	{"china mobile", "中国移动"},
	{"cmnet", "中国移动"},
	{"cernet", "中国教育网"},
	{"china education", "中国教育网"},
	{"alibaba", "阿里云"},
	{"aliyun", "阿里云"},
	{"tencent", "腾讯云"},
	{"huawei", "华为云"},
	{"baidu", "百度云"},
	{"ucloud", "UCloud"},
	{"kingsoft", "金山云"},
	{"bytedance", "火山引擎"},
	{"volcano", "火山引擎"},
	{"jdcloud", "京东云"},
	{"jd.com", "京东云"},
	{"chunghwa", "中华电信"},
	{"hinet", "中华电信"},
	{"data communication business group", "中华电信"},
	{"hong kong telecom", "HKT"},
	{"hkt limited", "HKT"},
	{"pccw", "HKT (PCCW)"},
	{"hgc global", "HGC"},
	{"hong kong broadband", "HKBN"},
	{"hkbn", "HKBN"},
	{"ntt", "NTT"},
	{"kddi", "KDDI"},
	{"softbank", "SoftBank"},
	{"internet initiative japan", "IIJ"},
	{"iij", "IIJ"},
	{"sk broadband", "SK Broadband"},
	{"korea telecom", "KT"},
	{"kt corp", "KT"},
	{"lg dacom", "LG U+"},
	{"lg uplus", "LG U+"},
	{"singtel", "Singtel"},
	{"singnet", "Singtel"},
	{"starhub", "StarHub"},
	{"telstra", "Telstra"},
	{"cloudflare", "Cloudflare"},
	{"google", "Google"},
	{"amazon", "Amazon AWS"},
	{"aws ", "Amazon AWS"},
	{"microsoft", "Microsoft Azure"},
	{"oracle", "Oracle Cloud"},
	{"digitalocean", "DigitalOcean"},
	{"linode", "Akamai (Linode)"},
	{"akamai", "Akamai"},
	{"vultr", "Vultr"},
	{"the constant company", "Vultr"},
	{"choopa", "Vultr"},
	{"hetzner", "Hetzner"},
	{"ovh", "OVH"},
	{"leaseweb", "Leaseweb"},
	{"contabo", "Contabo"},
	{"scaleway", "Scaleway"},
	{"online s.a.s", "Scaleway"},
	{"g-core", "Gcore"},
	{"gcore", "Gcore"},
	{"fastly", "Fastly"},
	{"zenlayer", "Zenlayer"},
	{"psychz", "Psychz"},
	{"multacom", "Multacom"},
	{"dmit", "DMIT"},
	{"it7 networks", "搬瓦工 (IT7)"},
	{"bandwagon", "搬瓦工 (IT7)"},
	{"racknerd", "RackNerd"},
	{"misaka", "Misaka"},
	{"xtom", "xTom"},
	{"cogent", "Cogent"},
	{"comcast", "Comcast"},
	{"at&t", "AT&T"},
	{"verizon", "Verizon"},
	{"hurricane electric", "Hurricane Electric"},
	{"level 3", "Lumen (Level 3)"},
	{"lumen", "Lumen"},
	{"gtt ", "GTT"},
	{"telia", "Arelion (Telia)"},
	{"arelion", "Arelion"},
	{"m247", "M247"},
	{"datacamp", "DataCamp"},
	{"cdn77", "CDN77"},
}

// legalSuffixes are trimmed from organisation names that have no alias.
var legalSuffixes = []string{
	", inc.", ", inc", " inc.", " inc", ", llc", " llc", ", ltd.", ", ltd", " ltd.", " ltd",
	" limited", " co., ltd.", " co., ltd", " co.,ltd", " co. ltd", " corporation", " corp.", " corp",
	" gmbh", " s.a.", " s.a", " b.v.", " b.v", " pty", " plc", " ag", " sas", " s.r.l.", " srl",
}

// shortenOrg turns an ASN organisation string into a short display name.
func shortenOrg(org string) string {
	org = strings.TrimSpace(org)
	if org == "" {
		return ""
	}
	lower := strings.ToLower(org) + " "
	for _, a := range orgAlias {
		if strings.Contains(lower, a.match) {
			return a.short
		}
	}
	// Drop a leading "AS12345 " prefix some databases include.
	if f := strings.Fields(org); len(f) > 1 && strings.HasPrefix(strings.ToUpper(f[0]), "AS") && isDigits(f[0][2:]) {
		org = strings.Join(f[1:], " ")
	}
	for changed := true; changed; {
		changed = false
		low := strings.ToLower(org)
		for _, s := range legalSuffixes {
			if strings.HasSuffix(low, s) {
				org = strings.TrimSpace(org[:len(org)-len(s)])
				org = strings.TrimRight(org, ",")
				changed = true
				break
			}
		}
	}
	return org
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
