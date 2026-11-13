package agent

import "testing"

// Intranet destinations are skipped unless the selection is allowed onto
// them; programs are never exempt; the user's own bypass always wins.
func TestPrivateExemption(t *testing.T) {
	a := &Agent{}
	cfg := compile(Config{Enabled: true,
		Domains:      []string{"portal.za.biz", "nas.corp", "plain.za.biz"},
		Bypass:       []string{"+.apple.com", "secret.za.biz"},
		Private:      []string{"10.0.0.0/8", "+.corp"},
		AllowPrivate: []string{"portal.za.biz", "nas.corp"},
	})
	dom := Selector{Kind: "domain"}
	app := Selector{Kind: "app"}
	cases := []struct {
		sel  Selector
		host string
		pass bool
	}{
		{dom, "portal.za.biz", false},
		{dom, "nas.corp", false},     // allowed .corp name
		{dom, "plain.za.biz", false}, // name only: no private entry hit
		{dom, "10.1.2.3", true},      // IP destination, not exempt
		{dom, "x.corp", true},        // not selected
		{app, "nas.corp", true},      // programs never exempt
		{dom, "secret.za.biz", true},
		{dom, "intranet", true}, // single label
	}
	for _, c := range cases {
		why, _ := a.decidePlain(cfg, c.sel, connectReq{host: c.host, port: 80})
		if (why != "") != c.pass {
			t.Errorf("%s (%s): why=%q, want pass=%v", c.host, c.sel.Kind, why, c.pass)
		}
	}
	// Update replaces the lists
	a.cfg.Store(cfg)
	none := []string{}
	a.Apply(Update{AllowPrivate: &none})
	if why, _ := a.decidePlain(a.cfg.Load(), dom, connectReq{host: "nas.corp", port: 80}); why == "" {
		t.Error("nas.corp decrypted after its exemption was removed")
	}
}
