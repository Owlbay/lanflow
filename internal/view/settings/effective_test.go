package settings

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// In 跟随订阅 mode the DNS and 嗅探 tabs show the subscription's own
// sections read-only, not Lanflow's stored draft (docs/design.md:536);
// taking over shows Lanflow's settings again.
func TestFollowModeShowsSubscriptionSections(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1200)
	if err := tt.Click("DNS"); err != nil {
		t.Fatal(err)
	}
	sub := f.EffectiveDNS().Settings
	if !tt.HasText("正在使用订阅「我的机场」中的 DNS 设置") {
		t.Fatalf("banner missing: %q", tt.Texts())
	}
	// The subscription's upstream servers are shown; Lanflow's stored ones are not.
	value := func(label string) string {
		info, ok := tt.AccessByRole(ui.RoleTextField, label)
		if !ok {
			t.Fatalf("no text field %q", label)
		}
		return info.Value
	}
	if got := value("上游服务器"); got != strings.Join(sub.Nameserver, "\n") {
		t.Fatalf("follow mode shows %q, want the subscription's nameservers", got)
	}
	if value("域名或规则集") != "geosite:cn" || value("服务器") != "223.5.5.5" {
		t.Fatalf("policy rows: %q → %q", value("域名或规则集"), value("服务器"))
	}
	if p.dnsDirty() {
		t.Fatal("showing the subscription's values must not count as an edit")
	}

	if err := tt.Click("由澜流接管"); err != nil {
		t.Fatal(err)
	}
	if got := value("上游服务器"); got != "https://doh.pub/dns-query\nhttps://dns.alidns.com/dns-query" {
		t.Fatalf("take-over shows %q, want Lanflow's own nameservers", got)
	}

	// 嗅探: the subscription sniffs TLS only on 443.
	p.discardDNS()
	if err := tt.Click("嗅探"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("正在使用订阅「我的机场」中的嗅探设置") || value("跳过嗅探的域名") != "Mijia Cloud" || value("TLS 端口") != "443" {
		t.Fatalf("sniffer follow mode: %q", tt.Texts())
	}
	if p.snfDirty() {
		t.Fatal("showing the subscription's sniffer must not count as an edit")
	}
}

// 上次更新 comes from the geo files: shipped copies say so, a download
// shows its time, and only without files does the manual time count.
func TestGeoUpdatedText(t *testing.T) {
	when := time.Date(2026, 3, 4, 5, 6, 0, 0, time.Local)
	if got := geoUpdatedText(model.GeoDataInfo{Files: 3, Bundled: true, UpdatedAt: when}, time.Time{}); got != "随应用附带" {
		t.Errorf("bundled = %q", got)
	}
	if got := geoUpdatedText(model.GeoDataInfo{Files: 3, UpdatedAt: when}, time.Time{}); got != "2026-03-04 05:06" {
		t.Errorf("downloaded = %q", got)
	}
	if got := geoUpdatedText(model.GeoDataInfo{}, when); got != "2026-03-04" {
		t.Errorf("manual fallback = %q", got)
	}
	if got := geoUpdatedText(model.GeoDataInfo{}, time.Time{}); got != "—" {
		t.Errorf("nothing = %q", got)
	}
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	if err := tt.Click("内核"); err != nil {
		t.Fatal(err)
	}
	want := f.GeoDataInfo().UpdatedAt.Local().Format("2006-01-02 15:04")
	if !tt.HasText(want) {
		t.Fatalf("上次更新 should show the files' time %q: %q", want, tt.Texts())
	}
}
