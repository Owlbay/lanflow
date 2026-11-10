package model

import "time"

// Feedback reporting (docs/feedback-api.md): errors are collected locally
// in errors.jsonl of the log directory; 反馈问题 builds a report the user
// previews before it is sent to the endpoint they configured, or saved
// to a file.

// Error record kinds.
const (
	ErrorKindError    = "error"     // an Error level line of Lanflow's run log
	ErrorKindCoreExit = "core-exit" // the core exited on its own
	ErrorKindPanic    = "panic"     // a Go panic or fatal runtime error
)

// ErrorRecord is one line of errors.jsonl. Message and Detail are redacted
// when recorded (applog.Redact).
type ErrorRecord struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`             // ErrorKind*
	Message  string    `json:"message"`          // one line
	Detail   string    `json:"detail,omitempty"` // stack, or the core's last log lines
	ExitCode *int      `json:"exitCode,omitempty"`
	Version  string    `json:"version"`
	Platform string    `json:"platform"` // GOOS/GOARCH
}

// FeedbackSettings configures where reports go. The token lives in the
// system keychain (API.SetFeedbackToken), not here.
type FeedbackSettings struct {
	// Endpoint is the base URL of the user's report service; reports are
	// POSTed to {Endpoint}/v1/reports. "" = reports can only be saved.
	Endpoint string `json:",omitempty"`
	// AutoReport sends a summary of new errors (no diagnostics, no log)
	// on its own; off by default.
	AutoReport bool `json:",omitempty"`
}

// FeedbackInfo is the cheap read behind the feedback settings and dialog.
type FeedbackInfo struct {
	Endpoint   string
	TokenSet   bool
	AutoReport bool
	Errors     int          // records in errors.jsonl
	LastCrash  *ErrorRecord // the previous run's crash, until the user answers
	LastSentAt time.Time    // last report sent (manual or automatic)
}

// FeedbackOptions is what the dialog asks BuildFeedback for.
type FeedbackOptions struct {
	Description string
	Contact     string // optional
	// IncludeDiagnostics attaches the redacted diagnostics bundle;
	// IncludeLog the recent part of lanflow.log.
	IncludeDiagnostics bool
	IncludeLog         bool
	// Errors is how many recent error records to include (0 = 50).
	Errors int
}

// Report kinds.
const (
	FeedbackManual = "manual"
	FeedbackAuto   = "auto"
)

// FeedbackReport is a built report, shown in full before it is sent.
type FeedbackReport struct {
	ID          string         `json:"id"` // UUID v4, also X-Lanflow-Report-Id
	Kind        string         `json:"kind"`
	CreatedAt   time.Time      `json:"createdAt"`
	Description string         `json:"description"`
	Contact     string         `json:"contact,omitempty"`
	App         FeedbackApp    `json:"app"`
	Errors      []ErrorRecord  `json:"errors"`
	Log         string         `json:"log,omitempty"`         // recent lanflow.log, redacted
	Attachments []FeedbackFile `json:"attachments,omitempty"` // the diagnostics bundle
	// Endpoint is where SendFeedback posts it ("" = not configured).
	Endpoint string `json:"endpoint,omitempty"`
	// Size is the size in bytes of the request body SendFeedback sends.
	Size int64 `json:"size"`
}

// FeedbackApp identifies the build.
type FeedbackApp struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	OSVersion string `json:"osVersion,omitempty"`
	Core      string `json:"core,omitempty"` // Mihomo version
	Language  string `json:"language,omitempty"`
}

// FeedbackFile is one file of the diagnostics bundle, in full.
type FeedbackFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"`
}

// FeedbackSent is the server's answer to SendFeedback.
type FeedbackSent struct {
	ID        string `json:"id"`
	URL       string `json:"url,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"` // the server had it already (200)
}
