package netinfo

import "strings"

// parseNetshWlan extracts the SSID from `netsh wlan show interfaces`. The
// output has one block per wireless interface; when name is non-empty the
// block whose "Name" matches is preferred, otherwise the first connected
// block wins. "BSSID" lines are ignored and a disconnected interface
// yields "".
func parseNetshWlan(out, name string) string {
	type block struct {
		name, ssid, state string
	}
	var blocks []block
	var cur *block
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case strings.EqualFold(k, "Name") || k == "名称":
			blocks = append(blocks, block{name: v})
			cur = &blocks[len(blocks)-1]
		case cur == nil:
		case strings.EqualFold(k, "SSID"):
			cur.ssid = v
		case strings.EqualFold(k, "State") || k == "状态":
			cur.state = strings.ToLower(v)
		}
	}
	// Prefer the block for the requested adapter.
	if name != "" {
		for _, b := range blocks {
			if strings.EqualFold(b.name, name) {
				return connectedSSID(b.ssid, b.state)
			}
		}
	}
	for _, b := range blocks {
		if s := connectedSSID(b.ssid, b.state); s != "" {
			return s
		}
	}
	return ""
}

func connectedSSID(ssid, state string) string {
	if strings.Contains(state, "disconnected") || strings.Contains(state, "已断开") {
		return ""
	}
	return ssid
}
