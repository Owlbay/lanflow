package app

import (
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/locate"
	"lanflow/internal/model"
)

// The words of the IP 类型 item (leak.go): locate.Kind decides what an
// address's network looks like from the bundled ASN database; these turn
// the kind and the endpoint into one line.

// ipKindText is the kind in words.
func ipKindText(kind string) string {
	switch kind {
	case model.IPKindDatacenter:
		return i18n.T("机房 IP")
	case model.IPKindResidential:
		return i18n.T("家宽 / 运营商 IP")
	}
	return i18n.T("类型未知")
}

// endpointLine is "203.0.113.9 · AS13335 Cloudflare · 机房 IP" for ep;
// missing parts are left out.
func endpointLine(ep model.EndpointInfo) string {
	parts := []string{ep.IP}
	if org := strings.TrimSpace(strings.TrimSpace(ep.ASN) + " " + ep.ISP); org != "" {
		parts = append(parts, org)
	}
	parts = append(parts, ipKindText(locate.Kind(ep.ASN, ep.ISP)))
	return strings.Join(parts, " · ")
}
