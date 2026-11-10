// Package redact masks credentials in captured requests (docs/capture.md
// §7.3) for display, HAR export and agents, and reduces a body to its
// structure (keys, types, lengths) for agents (§10).
package redact

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"lanflow/internal/model"
)

var sensitiveHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true, "set-cookie": true,
	"x-api-key": true, "x-auth-token": true, "x-csrf-token": true, "x-xsrf-token": true,
}

var sensitiveName = regexp.MustCompile(`(?i)(token|secret|session|password|passwd|signature|api[-_]?key|auth)`)

// SensitiveHeader reports a header masked by default.
func SensitiveHeader(name string) bool {
	return sensitiveHeaders[strings.ToLower(name)] || sensitiveName.MatchString(name)
}

// SensitiveKey reports a query parameter, form field or JSON key masked by default.
func SensitiveKey(name string) bool { return sensitiveName.MatchString(name) }

// Mask shows the first four characters and the length: "abcd…(32 B)";
// values of eight bytes or less show only the length.
func Mask(v string) string {
	if v == "" {
		return ""
	}
	n := 0
	cut := len(v)
	for i := range v {
		if n == 4 {
			cut = i
			break
		}
		n++
	}
	if len(v) <= 8 {
		cut = 0
	}
	return fmt.Sprintf("%s…(%d B)", v[:cut], len(v))
}

// Headers returns the headers with sensitive values masked.
func Headers(h []model.KV) []model.KV {
	out := make([]model.KV, len(h))
	for i, kv := range h {
		out[i] = kv
		if SensitiveHeader(kv.Key) {
			out[i].Value = Mask(kv.Value)
		}
	}
	return out
}

// Path masks sensitive query parameters of a request target.
func Path(p string) string {
	path, q, ok := strings.Cut(p, "?")
	if !ok {
		return p
	}
	parts := strings.Split(q, "&")
	for i, kv := range parts {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			name, _ := url.QueryUnescape(k)
			if SensitiveKey(name) {
				parts[i] = k + "=" + Mask(v)
			}
		}
	}
	return path + "?" + strings.Join(parts, "&")
}

// Decode undoes a Content-Encoding (gzip, deflate) for display; other
// encodings (br, zstd) return ok=false.
func Decode(body []byte, encoding string) ([]byte, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, true
	case "gzip", "x-gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, false
		}
		b, err := io.ReadAll(io.LimitReader(r, 16<<20))
		return b, err == nil || len(b) > 0
	case "deflate":
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if b, err := io.ReadAll(io.LimitReader(r, 16<<20)); err == nil || len(b) > 0 {
				return b, true
			}
		}
		b, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(body)), 16<<20))
		return b, err == nil || len(b) > 0
	}
	return nil, false
}

// Kind classifies a body by its Content-Type: "json", "form", "text",
// "image", "multipart" or "binary".
func Kind(contentType string, body []byte) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		return "json"
	case mt == "application/x-www-form-urlencoded":
		return "form"
	case strings.HasPrefix(mt, "multipart/"):
		return "multipart"
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case strings.HasPrefix(mt, "text/") || mt == "application/javascript" || mt == "application/xml" || strings.HasSuffix(mt, "+xml"):
		return "text"
	}
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') && json.Valid(body) {
		return "json"
	}
	if utf8.Valid(body) {
		return "text"
	}
	return "binary"
}

// Body masks sensitive values of a JSON or form body (other bodies are
// returned as they are: masking cannot cover arbitrary text, §7.3).
func Body(body []byte, contentType string) []byte {
	kind := Kind(contentType, body)
	if kind == "json" && !json.Valid(body) && looksForm(body) {
		kind = "form" // a mislabelled form
	}
	switch kind {
	case "json":
		var v any
		if json.Unmarshal(body, &v) != nil {
			return body
		}
		b, err := json.MarshalIndent(maskJSON(v), "", "  ")
		if err != nil {
			return body
		}
		return b
	case "form":
		// field by field, order kept, values shown unescaped
		parts := strings.Split(string(body), "&")
		for i, kv := range parts {
			k, v, ok := strings.Cut(kv, "=")
			name, err1 := url.QueryUnescape(k)
			val, err2 := url.QueryUnescape(v)
			if err1 != nil || err2 != nil {
				name, val = k, v
			}
			if ok && SensitiveKey(name) {
				val = Mask(val)
			}
			if ok {
				parts[i] = name + "=" + val
			} else {
				parts[i] = name
			}
		}
		return []byte(strings.Join(parts, "&"))
	}
	return body
}

func looksForm(b []byte) bool {
	return bytes.IndexByte(b, '=') > 0 && !bytes.ContainsAny(b, " \n\r\t{}\"")
}

func maskJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && SensitiveKey(k) {
				x[k] = Mask(s)
				continue
			}
			x[k] = maskJSON(val)
		}
	case []any:
		for i := range x {
			x[i] = maskJSON(x[i])
		}
	}
	return v
}

// Structure reduces a body to its shape: JSON keys with types and
// lengths, form field names with lengths, or the kind and size. No value
// leaves (docs/capture.md §10 L2).
func Structure(body []byte, contentType string) any {
	k := Kind(contentType, body)
	if k != "json" && len(body) > 0 && (body[0] == '{' || body[0] == '[') && json.Valid(body) {
		k = "json" // mislabelled
	}
	switch k {
	case "json":
		var v any
		if json.Unmarshal(body, &v) == nil {
			return map[string]any{"kind": "json", "size": len(body), "shape": shape(v, 0)}
		}
	case "form":
		if vals, err := url.ParseQuery(string(body)); err == nil && plainNames(vals) {
			fields := map[string]string{}
			for name, vs := range vals {
				n := 0
				for _, v := range vs {
					n += len(v)
				}
				fields[name] = fmt.Sprintf("string(%d)", n)
			}
			return map[string]any{"kind": "form", "size": len(body), "fields": fields}
		}
	default:
		return map[string]any{"kind": k, "size": len(body)}
	}
	return map[string]any{"kind": "text", "size": len(body)}
}

var plainName = regexp.MustCompile(`^[A-Za-z0-9_.\-\[\]]{1,64}$`)

// plainNames: form field names that are names, not data.
func plainNames(v url.Values) bool {
	for k := range v {
		if !plainName.MatchString(k) {
			return false
		}
	}
	return true
}

func shape(v any, depth int) any {
	if depth > 8 {
		return "…"
	}
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out[k] = shape(x[k], depth+1)
		}
		return out
	case []any:
		if len(x) == 0 {
			return "array(0)"
		}
		return map[string]any{"array": len(x), "item": shape(x[0], depth+1)}
	case string:
		return fmt.Sprintf("string(%d)", len(x))
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return "?"
}
