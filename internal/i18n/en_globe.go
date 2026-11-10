package i18n

// English of the 3D exit globe (internal/view/overview/exitmap.go), kept
// apart from en.go so that it can be edited alongside the globe.
func init() {
	for k, v := range map[string]string{
		"。拖动可旋转地球，滚轮缩放，双击复位": ". Drag to turn the globe, scroll to zoom, double-click to reset.",
		"放大":   "Zoom in",
		"缩小":   "Zoom out",
		"复位视角": "Reset view",
		"地球全景": "Whole globe",
	} {
		en[k] = v
	}
}
