package locate

import (
	"testing"

	"lanflow/internal/model"
)

// Kind: hosting by ASN or provider name, carriers by name, a carrier's
// cloud brand stays a carrier, and nothing known is unknown.
func TestKind(t *testing.T) {
	cases := []struct {
		asn, org, want string
	}{
		{"AS13335", "Cloudflare", model.IPKindDatacenter},
		{"as20473", "", model.IPKindDatacenter},
		{"AS64512", "Vultr", model.IPKindDatacenter},
		{"", "Hetzner Online GmbH", model.IPKindDatacenter},
		{"", "Some Random Hosting Ltd", model.IPKindDatacenter},
		{"AS4812", "中国电信", model.IPKindResidential},
		{"AS4134", "CHINANET-BACKBONE China Telecom", model.IPKindResidential},
		{"AS7922", "Comcast Cable Communications, LLC", model.IPKindResidential},
		{"AS9808", "China Mobile Communications Group", model.IPKindResidential},
		{"", "China Telecom Cloud", model.IPKindResidential},
		{"AS45102", "阿里云", model.IPKindDatacenter},
		{"", "阿里云", model.IPKindDatacenter},
		{"", shortenOrg("Alibaba (US) Technology Co., Ltd."), model.IPKindDatacenter},
		{"", "", model.IPKindUnknown},
		{"AS64496", "Example Networks", model.IPKindUnknown},
	}
	for _, c := range cases {
		if got := Kind(c.asn, c.org); got != c.want {
			t.Errorf("Kind(%q, %q) = %q, want %q", c.asn, c.org, got, c.want)
		}
	}
}
