package locate

import (
	"strings"

	"lanflow/internal/model"
)

// IP kind (the 网络 page's IP 类型 check): does an address belong to a
// cloud / hosting network, which streaming sites and risk engines flag as
// a proxy, or to a consumer ISP / mobile carrier? Decided offline from
// the ASN and organisation the bundled GeoLite2-ASN database gives
// (Lookup): a short list of well-known hosting ASNs first, then keywords
// of the organisation name (the full name or the short one shortenOrg
// produces, so the Chinese aliases of org.go are listed too). Nothing is
// looked up online.

// datacenterASNs are well-known cloud, hosting and CDN networks.
var datacenterASNs = map[string]bool{
	"AS13335":  true, // Cloudflare
	"AS15169":  true, // Google
	"AS396982": true, // Google Cloud
	"AS16509":  true, // Amazon AWS
	"AS14618":  true, // Amazon AWS (EC2 us-east)
	"AS8075":   true, // Microsoft Azure
	"AS45102":  true, // Alibaba Cloud (international)
	"AS37963":  true, // Alibaba Cloud (CN)
	"AS132203": true, // Tencent Cloud
	"AS45090":  true, // Tencent Cloud (CN)
	"AS136907": true, // Huawei Cloud
	"AS55990":  true, // Huawei Cloud (CN)
	"AS31898":  true, // Oracle Cloud
	"AS14061":  true, // DigitalOcean
	"AS63949":  true, // Akamai (Linode)
	"AS20940":  true, // Akamai
	"AS20473":  true, // Vultr
	"AS24940":  true, // Hetzner
	"AS16276":  true, // OVH
	"AS60781":  true, // Leaseweb
	"AS51167":  true, // Contabo
	"AS12876":  true, // Scaleway
	"AS9009":   true, // M247
	"AS60068":  true, // DataCamp
	"AS54113":  true, // Fastly
	"AS199524": true, // Gcore
	"AS21859":  true, // Zenlayer
	"AS40676":  true, // Psychz
	"AS35916":  true, // Multacom
	"AS906":    true, // DMIT
	"AS25820":  true, // IT7 (搬瓦工)
	"AS3214":   true, // xTom
	"AS135377": true, // UCloud
	"AS38365":  true, // Baidu Cloud
	"AS59019":  true, // Kingsoft Cloud
}

// datacenterWords are organisation-name fragments (lower case) of hosting
// networks; ispWords those of access networks.
var datacenterWords = []string{
	"cloud", "hosting", "host ", "hoster", "server", "data center", "datacenter", "data-center", "datacamp",
	"vps", "dedicated", "colocation", "colo ", "cdn", "akamai", "fastly", "cloudflare", "amazon", "aws",
	"google", "microsoft", "azure", "oracle", "digitalocean", "linode", "vultr", "choopa", "constant company",
	"hetzner", "ovh", "leaseweb", "m247", "contabo", "scaleway", "online s.a.s", "ionos", "godaddy",
	"zenlayer", "gcore", "g-core", "psychz", "quadranet", "multacom", "racknerd", "misaka", "xtom", "dmit",
	"it7", "bandwagon", "搬瓦工", "ucloud", "kamatera", "hostinger", "namecheap", "cdn77", "edgecast", "limelight",
	"阿里云", "腾讯云", "华为云", "百度云", "金山云", "火山引擎", "京东云",
}

var ispWords = []string{
	"telecom", "telekom", "telefonica", "telstra", "telia", "mobile", "wireless", "cellular", "unicom",
	"broadband", "cable", "communications", "comcast", "verizon", "at&t", "charter", "spectrum", "cox ",
	"vodafone", "orange", "ntt", "kddi", "softbank", "chunghwa", "hinet", "hkt", "pccw", "hgc", "hkbn",
	"sk broadband", "korea telecom", "lg u+", "lg dacom", "singtel", "starhub", "bt ", "british telecom",
	"sky ", "virgin media", "fiber", "fibre", "dsl", "isp", "internet service", "rogers", "bell canada",
	"telus", "optus", "deutsche", "free sas", "sfr", "bouygues", "swisscom", "kpn", "ziggo", "tim ", "iliad",
	"中国电信", "中国联通", "中国移动", "中国教育网", "中华电信", "广电", "铁通", "宽带",
}

// genericHostingWords describe a kind of network rather than naming a
// provider, so a carrier's cloud brand ("China Telecom Cloud") stays a
// carrier: provider names win, then ISP words, then these.
var genericHostingWords = map[string]bool{
	"cloud": true, "hosting": true, "host ": true, "hoster": true, "server": true, "data center": true, "datacenter": true,
	"data-center": true, "vps": true, "dedicated": true, "colocation": true, "colo ": true, "cdn": true,
}

// Kind returns the model.IPKind of an address with ASN asn (e.g.
// "AS13335", may be "") and organisation org (short or full name).
func Kind(asn, org string) string {
	if datacenterASNs[strings.ToUpper(strings.TrimSpace(asn))] {
		return model.IPKindDatacenter
	}
	low := strings.ToLower(strings.TrimSpace(org)) + " "
	if low == " " {
		return model.IPKindUnknown
	}
	for _, w := range datacenterWords {
		if !genericHostingWords[w] && strings.Contains(low, w) {
			return model.IPKindDatacenter
		}
	}
	for _, w := range ispWords {
		if strings.Contains(low, w) {
			return model.IPKindResidential
		}
	}
	for _, w := range datacenterWords {
		if strings.Contains(low, w) {
			return model.IPKindDatacenter
		}
	}
	return model.IPKindUnknown
}
