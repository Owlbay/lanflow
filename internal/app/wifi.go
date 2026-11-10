package app

import (
	"fmt"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Switching the subscription by Wi-Fi (Settings.SSIDRules): when the
// network the machine is on changes (watch.go recheck, or the watcher
// noticing a new SSID in the network information), the rule for the
// SSID names the subscription to use; networks without a rule fall back
// to Settings.SSIDDefaultSub when set. An unknown SSID (wired, or macOS
// hiding it without Location permission) changes nothing. The rules only
// run when the SSID changes, so a subscription the user picks by hand
// stays until the next network.

// wifiTarget picks the subscription for ssid: the matching rule's, else
// the default; "" when nothing applies.
func wifiTarget(set model.Settings, ssid string) string {
	ssid = strings.TrimSpace(ssid)
	if ssid == "" {
		return ""
	}
	for _, r := range set.SSIDRules {
		if strings.EqualFold(strings.TrimSpace(r.SSID), ssid) && r.SubscriptionID != "" {
			return r.SubscriptionID
		}
	}
	return set.SSIDDefaultSub
}

// wifiCheck applies the rules when the SSID of NetInfo changed since the
// last run. It runs off the UI thread (watch.go) and may switch the
// subscription, which takes the operation lock.
func (s *Service) wifiCheck() {
	s.mu.Lock()
	ssid := strings.TrimSpace(s.state.net.SSID)
	set := s.state.settings
	cur := s.state.st.CurrentSubscription
	s.mu.Unlock()

	st := feat2(s)
	st.mu.Lock()
	changed := !st.wifiInit || ssid != st.lastSSID
	st.wifiInit, st.lastSSID = true, ssid
	st.mu.Unlock()
	if !changed || (len(set.SSIDRules) == 0 && set.SSIDDefaultSub == "") {
		return
	}
	if ssid == "" {
		s.alog.Infof("wifi: network changed, SSID unknown; subscription rules skipped")
		return
	}
	target := wifiTarget(set, ssid)
	if target == "" || target == cur {
		return
	}
	sub, ok := s.d.store.Get(target)
	if !ok {
		s.alog.Warnf("wifi %q: rule points at a subscription that no longer exists (%s)", ssid, target)
		return
	}
	s.alog.Infof("wifi %q: switching to subscription %q", ssid, sub.Name)
	if err := s.UseSubscription(target); err != nil {
		s.notify(Notice{Kind: "error", Title: fmt.Sprintf(i18n.T("按 Wi-Fi「%s」切换订阅失败"), ssid), Detail: err.Error(), Subject: target})
		return
	}
	s.notify(Notice{Kind: "toast", Title: fmt.Sprintf(i18n.T("已连接 Wi-Fi「%s」，切换到订阅「%s」"), ssid, sub.Name), Subject: target})
}
