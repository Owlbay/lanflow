package i18n

// English of the strings added by the window-size and layout work
// (internal/view/shell.go, internal/view/netpage).
func init() {
	for k, v := range map[string]string{
		// ---- internal/view/netpage/page.go ----
		"经内核的解析器": "Through the core's resolver",
	} {
		en[k] = v
	}
}
