package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/platform/sysdns"
)

// dnsMarkerFile exists while Lanflow has pointed the system DNS at the
// TUN-captured resolver; it holds what to put back.
const dnsMarkerFile = "sysdns-set"

type dnsMarker struct {
	Servers []string     `json:"servers"`
	Saved   sysdns.Saved `json:"saved"`
}

func (s *Service) dnsMarkerPath() string { return filepath.Join(s.opt.DataDir, dnsMarkerFile) }

func (s *Service) readDNSMarker() (dnsMarker, bool) {
	data, err := os.ReadFile(s.dnsMarkerPath())
	if err != nil {
		return dnsMarker{}, false
	}
	var m dnsMarker
	if json.Unmarshal(data, &m) != nil {
		return dnsMarker{}, true // corrupt: nothing we can restore, but it is ours
	}
	return m, true
}

// syncDNS makes the system DNS match the TUN state: while our TUN runs (and
// the setting is on) it points at sysdns.TUNServers, otherwise whatever was
// there before is put back. Idempotent and best effort: a failure is
// reported as a notice, it never blocks TUN itself.
func (s *Service) syncDNS() {
	if s.d.dns == nil || !s.d.dns.Supported() {
		return
	}
	s.mu.Lock()
	// aTrust's intranet resolvers are per-domain (scutil supplemental
	// resolvers on its own interface) and are not touched by setting the
	// network services' DNS, so this stays on while aTrust runs.
	want := s.state.runningTUN && s.state.settings.TUNSetDNS
	s.mu.Unlock()
	if !want {
		s.releaseDNS()
		return
	}
	if m, ok := s.readDNSMarker(); ok && slices.Equal(m.Servers, sysdns.TUNServers) {
		return // already set by us
	}
	saved, err := s.d.dns.Snapshot()
	if err != nil {
		s.notify(Notice{Kind: "error", Title: i18n.T("未能设置系统 DNS"), Detail: i18n.Tf("读取当前 DNS 失败：%s。TUN 仍在运行，但局域网 DNS 的查询可能绕过澜流。", err.Error())})
		return
	}
	// Write the marker first: if we crash between the two steps the next
	// start still knows what to restore.
	data, _ := json.Marshal(dnsMarker{Servers: sysdns.TUNServers, Saved: saved})
	if err := writeFileAtomic(s.dnsMarkerPath(), data, 0o600); err != nil {
		return
	}
	if err := s.d.dns.Set(sysdns.TUNServers); err != nil {
		s.alog.Errorf("system DNS: set %v failed: %v", sysdns.TUNServers, err)
		_ = s.d.dns.Restore(saved, sysdns.TUNServers)
		_ = os.Remove(s.dnsMarkerPath())
		s.notify(Notice{Kind: "error", Title: i18n.T("未能设置系统 DNS"), Detail: i18n.Tf("%s。TUN 仍在运行，但局域网 DNS 的查询可能绕过澜流。", err.Error())})
		return
	}
	s.alog.Infof("system DNS: set to %v while TUN runs (saved %s)", sysdns.TUNServers, string(saved))
}

// releaseDNS puts the system DNS back if Lanflow changed it (services the
// user changed meanwhile are left alone) and forgets the marker.
func (s *Service) releaseDNS() {
	m, ok := s.readDNSMarker()
	if !ok {
		return
	}
	if s.d.dns != nil && s.d.dns.Supported() && len(m.Saved) > 0 {
		if err := s.d.dns.Restore(m.Saved, m.Servers); err != nil {
			s.notify(Notice{Kind: "error", Title: i18n.T("未能恢复系统 DNS"), Detail: i18n.Tf("%s。可在「系统设置 › 网络 › 详细信息 › DNS」中手动删除 %s。", err.Error(), strings.Join(m.Servers, i18n.T("、")))})
			return // keep the marker: retried on the next stop or launch
		}
	}
	_ = os.Remove(s.dnsMarkerPath())
	s.alog.Infof("system DNS: restored %s", string(m.Saved))
}
