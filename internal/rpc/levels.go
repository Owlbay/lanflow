package rpc

// methodLevels is the permission level of every app.API method
// (docs/service-split.md §4). It is the source the generator
// (internal/app/remote/apigen) reads to fill Level in manifest_gen.go, and a
// test fails when an API method is missing here.
//
// Harmless actions that change no configuration (delay tests, probes,
// refreshes, DNS queries) count as reads: they only measure.
var methodLevels = map[string]Level{
	// ---- reads ----
	"Status":                 LevelRead,
	"TrafficHistory":         LevelRead,
	"Proxies":                LevelRead,
	"Subscriptions":          LevelRead, // URLs removed for agents
	"Connections":            LevelSensitive,
	"ClosedConnections":      LevelSensitive,
	"Logs":                   LevelSensitive,
	"ConnectionsSince":       LevelSensitive,
	"ClosedConnectionsSince": LevelSensitive,
	"LogsSince":              LevelSensitive,
	"AppLogsSince":           LevelRead, // redacted when written, like AppLogs
	"Rules":                  LevelRead,
	"CustomRules":            LevelRead,
	"RuleProviders":          LevelRead,
	"ExitRoutes":             LevelRead,
	"You":                    LevelRead,
	"NetInfo":                LevelRead,
	"Sites":                  LevelRead,
	"Settings":               LevelRead, // redacted for agents
	"Overrides":              LevelRead,
	"TakeNotices":            LevelUI, // clears the queue the window shows
	"ConfigDir":              LevelRead,
	"LogDir":                 LevelRead,
	"OnChange":               LevelRead, // served as subscribe

	// ---- core & switches ----
	"StartCore":          LevelWrite,
	"StopCore":           LevelWrite,
	"RestartCore":        LevelWrite,
	"SetSystemProxy":     LevelWrite,
	"SetTUN":             LevelWrite,
	"TUNHelperInstalled": LevelRead,
	"InstallTUNHelper":   LevelDanger,
	"UninstallTUNHelper": LevelDanger,
	"SetMode":            LevelWrite,
	"ResolveRecovery":    LevelUI,

	// ---- proxies ----
	"SelectProxy":         LevelWrite,
	"TestGroupDelay":      LevelRead,
	"TestProxyDelay":      LevelRead,
	"TestAllDelays":       LevelRead,
	"SetMainGroup":        LevelWrite,
	"ProxyProviders":      LevelRead,
	"UpdateProxyProvider": LevelWrite,
	"HealthCheckProvider": LevelRead,
	"IconData":            LevelRead,

	// ---- UI state ----
	"UIState":       LevelUI,
	"SaveUIState":   LevelUI,
	"UpdateUIState": LevelUI,

	// ---- subscriptions ----
	"ImportSubscriptionURL":  LevelWrite,
	"ImportSubscriptionFile": LevelWrite,
	"UpdateSubscription":     LevelWrite,
	"UpdateAllSubscriptions": LevelWrite,
	"UseSubscription":        LevelWrite,
	"EditSubscription":       LevelWrite,
	"DeleteSubscription":     LevelDanger,
	"SubscriptionRaw":        LevelSensitive,

	// ---- connections & logs ----
	"CloseConnection":     LevelWrite,
	"CloseAllConnections": LevelWrite,
	"ClearLogs":           LevelWrite,
	"ProcessIcon":         LevelRead,

	// ---- traffic statistics ----
	"TrafficStats":      LevelRead,
	"ResetTrafficStats": LevelDanger,

	// ---- rules ----
	"SaveCustomRule":     LevelWrite,
	"DeleteCustomRule":   LevelWrite,
	"MoveCustomRule":     LevelWrite,
	"SetRuleDisabled":    LevelWrite,
	"TestRule":           LevelRead,
	"UpdateRuleProvider": LevelWrite,

	// ---- exits & network ----
	"ProbeExits":       LevelRead,
	"RefreshNetwork":   LevelRead,
	"TestSites":        LevelRead,
	"DNSQuery":         LevelRead,
	"FlushDNSCache":    LevelWrite,
	"FlushFakeIPCache": LevelWrite,
	"LookupIP":         LevelRead,
	"DismissProxyHint": LevelUI,
	"LeakCheck":        LevelRead,
	"RunLeakCheck":     LevelRead,
	"RunLeakCheckWith": LevelRead,

	// ---- 检测 page ----
	"Probe":        LevelRead,
	"ProbeRegions": LevelRead,
	"ProbeIP":      LevelRead,
	"ProbeUnlock":  LevelRead,
	"ProbeRegion":  LevelRead,

	// ---- settings ----
	"EffectiveDNS":     LevelRead,
	"EffectiveSniffer": LevelRead,
	"GeoDataInfo":      LevelRead,
	"SaveSettings":     LevelWrite, // agents use settings.patch instead
	"ApplyDNS":         LevelWrite, // agents use settings.patch instead
	"ApplySniffer":     LevelWrite, // agents use settings.patch instead
	"SaveOverride":     LevelWrite,
	"PreviewOverride":  LevelRead,

	// ---- override list ----
	"OverrideList":        LevelRead,
	"SaveOverrideEntry":   LevelWrite,
	"DeleteOverrideEntry": LevelWrite,
	"MoveOverrideEntry":   LevelWrite,
	"UpdateOverrideEntry": LevelWrite,
	"RuntimeConfig":       LevelSensitive, // carries node servers and passwords
	"UpdateGeoData":       LevelWrite,

	// ---- core versions ----
	"CoreReleases":    LevelRead,
	"InstalledCores":  LevelRead,
	"InstallCore":     LevelDanger,
	"UseCore":         LevelDanger,
	"RemoveCore":      LevelDanger,
	"CheckCoreUpdate": LevelRead,
	"CoreDownload":    LevelRead,
	"DownloadCore":    LevelDanger,

	// ---- settings export / import ----
	"ExportSettings": LevelDanger,
	"ImportSettings": LevelDanger,

	// ---- configuration sync ----
	"ConfigSyncStatus":      LevelRead,
	"SetConfigSync":         LevelDanger, // writes outside the data directory
	"ResolveConfigConflict": LevelDanger,
	"ApplyConfigFile":       LevelWrite, // like the settings writes; undo restores settings, rules and overrides
	"SetSyncPassphrase":     LevelUI,    // the passphrase is typed in the window only, never by an agent
	"UnlockSync":            LevelUI,
	"ForgetSyncPassphrase":  LevelUI,

	// ---- aTrust ----
	"ATrustReference":    LevelRead,
	"ImportATrustConfig": LevelSensitive, // reads a local file
	"ExportATrustConfig": LevelWrite,

	// ---- mirror ----
	"TestMirrors": LevelRead,

	// ---- WebDAV backup ----
	"WebDAVInfo":        LevelRead,
	"SetWebDAVPassword": LevelDanger,
	"TestWebDAV":        LevelRead,
	"BackupNow":         LevelDanger,
	"ListBackups":       LevelRead,
	"RestoreBackup":     LevelDanger,
	"DeleteBackup":      LevelDanger,

	// ---- Lanflow's own log & diagnostics ----
	"AppLogs":           LevelRead, // redacted when written
	"ClearAppLogs":      LevelWrite,
	"ExportDiagnostics": LevelWrite,

	// ---- 反馈问题 ----
	// Agents may draft a report (BuildFeedback); only the window sends it,
	// after the user has seen it.
	"FeedbackInfo":     LevelRead,
	"SetFeedbackToken": LevelUI,
	"DismissLastCrash": LevelUI,
	"BuildFeedback":    LevelRead, // redacted like the diagnostics bundle
	"SendFeedback":     LevelUI,
	"SaveFeedback":     LevelWrite,

	// ---- HTTPS 选择性解密 (docs/capture.md §10) ----
	// Agents read the state, the record summaries (sensitive: asked per
	// connection) and one record's structure (headers masked, bodies as
	// keys / types / lengths, never values); they cannot turn decryption
	// on, change the selection or touch the root: danger (MCP refused,
	// ctl confirmed every time) or window only.
	"CaptureStatus":       LevelRead,
	"CaptureFlows":        LevelSensitive,
	"CaptureFlow":         LevelSensitive,
	"SetCapture":          LevelDanger,
	"ExtendCapture":       LevelDanger,
	"SaveCaptureSettings": LevelDanger,
	"AddCaptureDomain":    LevelDanger,
	"AddCaptureApp":       LevelDanger,
	"ClearCaptureFlows":   LevelWrite,
	"InstallCACert":       LevelDanger,
	"RegenerateCA":        LevelDanger,
	"RemoveCACert":        LevelDanger,
	"ExportCACert":        LevelUI,
	"ExportHAR":           LevelUI,

	"Shutdown": LevelDanger,
}
