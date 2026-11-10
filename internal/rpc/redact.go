package rpc

import (
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	"lanflow/internal/app"
	"lanflow/internal/applog"
	"lanflow/internal/model"
)

// What agents get back (docs/service-split.md §4): settings and
// subscriptions pass the diagnostics redaction (applog.Redact) and
// subscription links never leave the app.

// binaryResults are methods whose results are image bytes, not text.
var binaryResults = map[string]bool{"IconData": true, "ProcessIcon": true}

// redactResults prepares the results of method for an agent.
func redactResults(method string, res []any) []any {
	if binaryResults[method] {
		return res
	}
	out := make([]any, len(res))
	for i, v := range res {
		out[i] = RedactValue(v)
	}
	return out
}

// RedactValue removes subscription links, masks credentials and passes
// every string through applog.Redact. The result is the JSON form of v
// (maps, slices, strings, numbers).
func RedactValue(v any) any {
	switch x := v.(type) {
	case model.Settings:
		v = redactSettings(x)
	case model.Subscription:
		v = redactSub(x)
	case []model.Subscription:
		subs := make([]model.Subscription, len(x))
		for i, s := range x {
			subs[i] = redactSub(s)
		}
		v = subs
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var j any
	if err := json.Unmarshal(b, &j); err != nil {
		return nil
	}
	return walkStrings(j, applog.Redact)
}

func redactSub(s model.Subscription) model.Subscription {
	s.URL = ""
	return s
}

func redactSettings(s model.Settings) model.Settings {
	s.UI = model.UIState{}
	s.AgentGrants = nil
	s.ConnHiddenColumns = nil
	a := s.Advanced.Clone()
	for i := range a.Authentication {
		if u, _, ok := strings.Cut(a.Authentication[i], ":"); ok {
			a.Authentication[i] = u + ":***"
		} else {
			a.Authentication[i] = "***"
		}
	}
	s.Advanced = a
	return s
}

func walkStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case string:
		return f(x)
	case []any:
		for i := range x {
			x[i] = walkStrings(x[i], f)
		}
	case map[string]any:
		for k := range x {
			x[k] = walkStrings(x[k], f)
		}
	}
	return v
}

// keepSubscriptionURL fills the URL an agent could not see (it reads
// subscriptions without links) from the stored subscription, so editing a
// name or the update interval keeps the link.
func keepSubscriptionURL(api app.API, in model.Subscription) model.Subscription {
	if in.URL != "" {
		return in
	}
	for _, s := range api.Subscriptions() {
		if s.ID == in.ID {
			in.URL = s.URL
		}
	}
	return in
}

// summarize is a short, redacted description of a call for the audit log
// and the consent dialog.
func summarize(method string, args []reflect.Value) string {
	parts := make([]any, len(args))
	for i, a := range args {
		parts[i] = a.Interface()
	}
	return summarizeAny(method, parts)
}

func summarizeAny(method string, args []any) string {
	b, _ := json.Marshal(RedactValue(args))
	s := string(b)
	if s == "[]" || s == "null" {
		return method + "()"
	}
	s = method + "(" + strings.TrimSuffix(strings.TrimPrefix(s, "["), "]") + ")"
	return truncate(s, 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
