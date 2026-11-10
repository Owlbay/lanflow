package env

import (
	"fmt"
	"time"

	"lanflow/internal/i18n"
)

// FmtSpeed formats bytes per second: "0 B/s", "12.4 KB/s", "1.23 MB/s".
func FmtSpeed(b int64) string { return FmtBytes(b) + "/s" }

// FmtInt formats a count with thousands separators ("20,119"), for
// request and rule counts beside the tabular figures (docs/design.md §3.2).
func FmtInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	if len(s) > 3 {
		var b []byte
		pre := len(s) % 3
		if pre > 0 {
			b = append(b, s[:pre]...)
		}
		for i := pre; i < len(s); i += 3 {
			if len(b) > 0 {
				b = append(b, ',')
			}
			b = append(b, s[i:i+3]...)
		}
		s = string(b)
	}
	if neg {
		return "-" + s
	}
	return s
}

// FmtBytes formats a size with 1024 steps and one decimal from KB up, at
// every magnitude ("1.0 GB", not "1.02 GB"; docs/design.md §10).
func FmtBytes(b int64) string {
	switch {
	case b < 1024:
		return fmt.Sprintf("%d B", b)
	case b < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	case b < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b < 1<<40:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%.1f TB", float64(b)/(1<<40))
	}
}

// FmtGB formats bytes as GB with one decimal, without unit ("32.5").
func FmtGB(b int64) string { return fmt.Sprintf("%.1f", float64(b)/(1<<30)) }

// FmtDelay formats a delay for plain text.
func FmtDelay(ms int, testing bool) string {
	switch {
	case testing:
		return "…"
	case ms < 0:
		return i18n.T("超时")
	case ms == 0:
		return "—"
	default:
		return fmt.Sprintf("%d ms", ms)
	}
}

// DelayKind returns "green", "orange", "red" or "gray" for a delay.
func DelayKind(ms int) string {
	switch {
	case ms == 0:
		return "gray"
	case ms < 0 || ms > 800:
		return "red"
	case ms > 300:
		return "orange"
	default:
		return "green"
	}
}

// FmtDuration formats a duration as "0:05", "2:13", "1:02:03".
func FmtDuration(d time.Duration) string {
	s := int(d.Seconds())
	h, m, sec := s/3600, s%3600/60, s%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%d:%02d", m, sec)
}

// FmtUptime formats "2 小时 13 分" / "5 分钟".
func FmtUptime(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf(i18n.T("%d 小时 %d 分"), h, m)
	}
	return fmt.Sprintf(i18n.T("%d 分钟"), m)
}

// FmtAgo formats a past time: "刚刚", "5 分钟前", "今天 10:21", "昨天", "2026-11-01"; "—" for zero.
func FmtAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	now := time.Now()
	switch {
	case d < time.Minute:
		return i18n.T("刚刚")
	case d < time.Hour:
		return fmt.Sprintf(i18n.T("%d 分钟前"), int(d.Minutes()))
	case t.YearDay() == now.YearDay() && t.Year() == now.Year():
		return i18n.T("今天 ") + t.Format("15:04")
	case t.Year() == now.Year() && now.YearDay()-t.YearDay() == 1:
		return i18n.T("昨天")
	default:
		return t.Format("2006-01-02")
	}
}

// RuleTypeName returns the Chinese name of a custom rule type.
func RuleTypeName(t string) string {
	return map[string]string{"DOMAIN-SUFFIX": i18n.T("域名后缀"), "DOMAIN": i18n.T("完整域名"), "DOMAIN-KEYWORD": i18n.T("域名关键字"), "IP-CIDR": i18n.T("IP 段"), "PROCESS-NAME": i18n.T("进程名")}[t]
}

// TargetName shows DIRECT/REJECT in Chinese.
func TargetName(t string) string {
	switch t {
	case "DIRECT":
		return i18n.T("直连")
	case "REJECT":
		return i18n.T("拒绝")
	}
	return t
}
