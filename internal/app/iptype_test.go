package app

import (
	"testing"

	"lanflow/internal/model"
)

// endpointLine joins the address, its ASN / organisation and its kind.
func TestEndpointLine(t *testing.T) {
	if got := endpointLine(model.EndpointInfo{IP: "203.0.113.9", ASN: "AS13335", ISP: "Cloudflare"}); got != "203.0.113.9 · AS13335 Cloudflare · 机房 IP" {
		t.Errorf("endpointLine = %q", got)
	}
	if got := endpointLine(model.EndpointInfo{IP: "116.227.38.12", ASN: "AS4812", ISP: "中国电信"}); got != "116.227.38.12 · AS4812 中国电信 · 家宽 / 运营商 IP" {
		t.Errorf("carrier endpointLine = %q", got)
	}
	if got := endpointLine(model.EndpointInfo{IP: "203.0.113.9"}); got != "203.0.113.9 · 类型未知" {
		t.Errorf("bare endpointLine = %q", got)
	}
}
