// Package har writes captured requests as HAR 1.2 (docs/capture.md §7.4,
// M3): credentials masked as on screen; bodies only when asked for, and
// then with JSON / form credentials masked; Lanflow's own fields (process,
// selection, flags) under "_lanflow".
package har

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"lanflow/internal/capture/redact"
	"lanflow/internal/model"
)

type nv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

type postData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
	Comment  string `json:"comment,omitempty"`
}

type request struct {
	Method      string    `json:"method"`
	URL         string    `json:"url"`
	HTTPVersion string    `json:"httpVersion"`
	Cookies     []nv      `json:"cookies"`
	Headers     []nv      `json:"headers"`
	QueryString []nv      `json:"queryString"`
	PostData    *postData `json:"postData,omitempty"`
	HeadersSize int       `json:"headersSize"`
	BodySize    int64     `json:"bodySize"`
}

type response struct {
	Status      int     `json:"status"`
	StatusText  string  `json:"statusText"`
	HTTPVersion string  `json:"httpVersion"`
	Cookies     []nv    `json:"cookies"`
	Headers     []nv    `json:"headers"`
	Content     content `json:"content"`
	RedirectURL string  `json:"redirectURL"`
	HeadersSize int     `json:"headersSize"`
	BodySize    int64   `json:"bodySize"`
}

type timings struct {
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
}

type lanflowExt struct {
	Process     string   `json:"process,omitempty"`
	ProcessPath string   `json:"processPath,omitempty"`
	Attribution string   `json:"attribution,omitempty"`
	Selector    string   `json:"selector,omitempty"`
	Flags       []string `json:"flags,omitempty"`
	TLS         any      `json:"tls,omitempty"`
}

type entry struct {
	StartedDateTime string     `json:"startedDateTime"`
	Time            float64    `json:"time"`
	Request         request    `json:"request"`
	Response        response   `json:"response"`
	Cache           struct{}   `json:"cache"`
	Timings         timings    `json:"timings"`
	Lanflow         lanflowExt `json:"_lanflow"`
}

type creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type log struct {
	Version string  `json:"version"`
	Creator creator `json:"creator"`
	Entries []entry `json:"entries"`
}

// Build returns the HAR document of the decrypted requests among ds.
func Build(ds []model.CaptureFlowDetail, withBodies bool, version string) ([]byte, error) {
	l := log{Version: "1.2", Creator: creator{Name: "Lanflow", Version: version}, Entries: []entry{}}
	for _, d := range ds {
		f := d.Flow
		if f.Method == "" || !f.Has(model.FlowDecrypted) {
			continue
		}
		httpVer := "HTTP/1.1"
		if f.Proto == "h2" {
			httpVer = "HTTP/2"
		}
		scheme := f.Scheme
		if scheme == "" {
			scheme = "https"
		}
		host := f.Host
		if !(scheme == "https" && f.Port == 443 || scheme == "http" && f.Port == 80) && f.Port > 0 {
			host = host + ":" + itoa(f.Port)
		}
		path := redact.Path(f.Path)
		e := entry{
			StartedDateTime: f.Start.UTC().Format(time.RFC3339Nano),
			Time:            float64(f.DurationMs),
			Request: request{Method: f.Method, URL: scheme + "://" + host + path, HTTPVersion: httpVer,
				Cookies: []nv{}, Headers: headers(d.ReqHeader), QueryString: query(path), HeadersSize: -1, BodySize: d.ReqBodySize},
			Response: response{Status: f.Status, HTTPVersion: httpVer, Cookies: []nv{}, Headers: headers(d.RespHeader),
				HeadersSize: -1, BodySize: d.RespBodySize, RedirectURL: ""},
			Timings: timings{Send: 0, Wait: float64(d.Timing.TTFBMs), Receive: float64(max(0, d.Timing.EndMs-d.Timing.TTFBMs))},
			Lanflow: lanflowExt{Process: f.Process, ProcessPath: f.ProcessPath, Attribution: f.Attribution, Selector: f.Selector, Flags: f.Flags, TLS: d.TLS},
		}
		e.Response.Content = content{Size: d.RespBodySize, MimeType: f.ContentType}
		if withBodies {
			if len(d.ReqBody) > 0 || d.ReqBodySize > 0 {
				ct := hv(d.ReqHeader, "content-type")
				body, _ := redact.Decode(d.ReqBody, hv(d.ReqHeader, "content-encoding"))
				pd := &postData{MimeType: ct, Text: string(redact.Body(body, ct))}
				if d.ReqBodySize > int64(len(d.ReqBody)) {
					pd.Comment = "truncated"
				}
				e.Request.PostData = pd
			}
			if len(d.RespBody) > 0 {
				body, ok := redact.Decode(d.RespBody, hv(d.RespHeader, "content-encoding"))
				if !ok {
					e.Response.Content.Text = base64.StdEncoding.EncodeToString(d.RespBody)
					e.Response.Content.Encoding = "base64"
				} else if utf8.Valid(body) {
					e.Response.Content.Text = string(redact.Body(body, f.ContentType))
				} else {
					e.Response.Content.Text = base64.StdEncoding.EncodeToString(body)
					e.Response.Content.Encoding = "base64"
				}
				if d.RespBodySize > int64(len(d.RespBody)) {
					e.Response.Content.Comment = "truncated"
				}
			}
		} else {
			e.Response.Content.Comment = "bodies not exported"
		}
		l.Entries = append(l.Entries, e)
	}
	return json.MarshalIndent(map[string]any{"log": l}, "", "  ")
}

func headers(h []model.KV) []nv {
	out := []nv{}
	for _, kv := range redact.Headers(h) {
		out = append(out, nv{kv.Key, kv.Value})
	}
	return out
}

func query(path string) []nv {
	out := []nv{}
	_, q, ok := strings.Cut(path, "?")
	if !ok {
		return out
	}
	for _, kv := range strings.Split(q, "&") {
		k, v, _ := strings.Cut(kv, "=")
		out = append(out, nv{k, v})
	}
	return out
}

func hv(h []model.KV, name string) string {
	for _, kv := range h {
		if strings.EqualFold(kv.Key, name) {
			return kv.Value
		}
	}
	return ""
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
