// The launch-time check for a new version of the app.

package view

import (
	"log"
	"os"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
	"lanflow/internal/view/settings"
)

// CheckUpdatesAtLaunch checks for a new version of the app once a day,
// when the build can update itself and the setting allows (docs/design.md
// §9 应用自动更新). A found version is announced with a toast that opens
// the settings; the check itself runs off the UI thread.
func (s *Shell) CheckUpdatesAtLaunch() {
	if s.api.Settings().DisableUpdateCheck {
		return
	}
	page, ok := s.pages[env.Settings].(*settings.Page)
	if !ok || !page.UpdatesEnabled() || !s.updateCheckDue(time.Now()) {
		return
	}
	page.CheckAppUpdate(true, func(version string) {
		s.toast(toastReq{msg: i18n.Tf("澜流有新版本 %s", version), detail: i18n.T("在「设置 › 常规 › 关于」中下载并重启。"), action: i18n.T("前往设置"),
			onAction: func() { s.post(func() { s.navigate(env.Settings, "general") }) }})
	})
}

// updateCheckInterval is how often the launch-time update check runs.
const updateCheckInterval = 24 * time.Hour

// updateCheckDue reports whether the daily check is due at now, and
// records now as the last check when it is. Without a stamp file every
// launch checks.
func (s *Shell) updateCheckDue(now time.Time) bool {
	if s.updateStamp == "" {
		return true
	}
	if data, err := os.ReadFile(s.updateStamp); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil {
			if d := now.Sub(last); d >= 0 && d < updateCheckInterval {
				return false
			}
		}
	}
	if err := os.WriteFile(s.updateStamp, []byte(now.Format(time.RFC3339)+"\n"), 0o600); err != nil {
		log.Printf("lanflow: update check marker: %v", err)
	}
	return true
}
