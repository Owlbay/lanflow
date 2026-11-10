// Package i18n is the message catalog of 澜流 Lanflow (docs/design.md §5.8
// 「语言」). The source language is Simplified Chinese: a message's key is
// its Chinese text, so call sites stay readable, and en.go maps each key
// to English. T looks a key up in the language the whole process shows,
// which the service sets from Settings.Language (and the shell follows), so
// the view, the tray and the service's own error and notice texts switch
// together, without a restart.
//
//	ui.Text(c, i18n.T("代理端口"))
//	fmt.Errorf(i18n.T("端口 %d 已被占用"), port)   // or i18n.Tf
//
// Names from data (proxies, groups, subscriptions, rule payloads, the
// core's log lines, YAML) are never translated.
package i18n

import (
	"fmt"
	"strings"
	"sync/atomic"
)

// Language codes as Settings.Language stores them.
const (
	ZH = "zh"
	EN = "en"
)

// Languages lists the codes the app ships, source language first.
var Languages = []string{ZH, EN}

var current atomic.Pointer[string]

// Lang returns the language the process shows: ZH or EN.
func Lang() string {
	if p := current.Load(); p != nil {
		return *p
	}
	return ZH
}

// SetLang makes T say lang (ZH, EN, or any tag Normalize understands;
// anything else means ZH). It is safe from any goroutine; a view built
// afterwards shows the new language.
func SetLang(lang string) {
	l := Normalize(lang)
	current.Store(&l)
}

// Normalize maps a language tag or code to ZH or EN: "en", "en-US" and
// "English" are EN; "zh", "zh-Hans-CN" and anything unknown are ZH.
func Normalize(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == EN || strings.HasPrefix(l, "en-") || strings.HasPrefix(l, "en_") || l == "english" {
		return EN
	}
	return ZH
}

// FromSystem picks the language of a first run from the user's preferred
// languages, most preferred first (mygo.App.PreferredLanguages): Chinese
// when the first is a Chinese tag (zh, zh-Hans-CN, zh-TW…), else English.
// An empty list, a system that tells nothing, keeps ZH.
func FromSystem(preferred []string) string {
	if len(preferred) == 0 {
		return ZH
	}
	l := strings.ToLower(strings.TrimSpace(preferred[0]))
	if l == "zh" || strings.HasPrefix(l, "zh-") || strings.HasPrefix(l, "zh_") {
		return ZH
	}
	return EN
}

// Tag returns the BCP 47 tag MyGo knows a language by, for
// mygo.App.SetLocale: "zh-Hans" or "en".
func Tag(lang string) string {
	if Normalize(lang) == EN {
		return "en"
	}
	return "zh-Hans"
}

// Autonym names a language in itself, for the language selector.
func Autonym(lang string) string {
	if Normalize(lang) == EN {
		return "English"
	}
	return "简体中文"
}

// T returns key in the language the process shows. A key without an
// English entry is returned as is (the Chinese source), so a missing
// translation shows text rather than nothing; the catalog test keeps the
// table complete.
//
// A key may carry a context after a "|", as "规则|页" for the page named
// 规则 where the bare word also names a table column: the Chinese shown
// is the part before the "|", the English entry is looked up by the whole
// key, so one Chinese word can have two English translations.
func T(key string) string {
	if Lang() == EN {
		if s, ok := en[key]; ok {
			return s
		}
	}
	if i := strings.IndexByte(key, '|'); i >= 0 {
		return key[:i]
	}
	return key
}

// Tf is fmt.Sprintf(T(key), args...): the translated text is the format.
func Tf(key string, args ...any) string {
	return fmt.Sprintf(T(key), args...)
}

// Options translates keys for a Select or Segmented control; KeyOf maps
// the chosen option back.
func Options(keys ...string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = T(k)
	}
	return out
}

// KeyOf returns the key of keys whose translation is value, or value
// itself when none matches (a key stored before a language change).
func KeyOf(value string, keys []string) string {
	for _, k := range keys {
		if T(k) == value || k == value {
			return k
		}
	}
	return value
}

// lazyError is an error whose text is translated when it is read, so a
// package-level sentinel (var ErrX = i18n.Error("…")) speaks the language
// of the moment, not of init time. Two sentinels of the same text are
// equal, as errors.Is expects.
type lazyError string

func (e lazyError) Error() string { return T(string(e)) }

// Error returns a sentinel error whose Error() is T(key).
func Error(key string) error { return lazyError(key) }

// Has reports whether key has an English entry (tests).
func Has(key string) bool { _, ok := en[key]; return ok }

// Keys returns every key of the English table (tests).
func Keys() []string {
	out := make([]string, 0, len(en))
	for k := range en {
		out = append(out, k)
	}
	return out
}
