package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"lanflow/internal/i18n"
)

// A minimal WebDAV client over net/http for the backup feature
// (backup.go): PUT, GET, DELETE, MKCOL and PROPFIND with Basic auth.
// Nothing else of RFC 4918 is needed: Lanflow only keeps a flat
// directory of JSON files.

const (
	webdavTimeout  = 60 * time.Second
	maxBackupBytes = 16 << 20 // refuse a download larger than this
)

// webdavClient talks to one server.
type webdavClient struct {
	base *url.URL // server URL; its path is the root every call is relative to
	user string
	pass string
	http *http.Client
}

// newWebDAVClient validates rawURL and returns a client using c.
func newWebDAVClient(rawURL, user, pass string, c *http.Client) (*webdavClient, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New(i18n.T("WebDAV 地址须是以 http:// 或 https:// 开头的完整地址"))
	}
	u.RawQuery, u.Fragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return &webdavClient{base: u, user: user, pass: pass, http: c}, nil
}

// href returns the absolute URL of p (segments relative to the root,
// each escaped).
func (c *webdavClient) href(p string) string {
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/")
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg != "" {
			u.Path += "/" + seg
		}
	}
	u.RawPath = ""
	return u.String()
}

// do sends one request and checks the status against ok.
func (c *webdavClient) do(ctx context.Context, method, p string, body []byte, hdr map[string]string, ok ...int) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.href(p), r)
	if err != nil {
		return nil, err
	}
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("User-Agent", "Lanflow")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New(i18n.T("连接 WebDAV 服务器超时"))
		}
		return nil, fmt.Errorf(i18n.T("无法连接 WebDAV 服务器：%v"), err)
	}
	for _, s := range ok {
		if resp.StatusCode == s {
			return resp, nil
		}
	}
	resp.Body.Close()
	return nil, statusError(resp.StatusCode)
}

// statusError words an unexpected status.
func statusError(code int) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errors.New(i18n.T("WebDAV 登录被拒绝，请检查用户名和密码"))
	case http.StatusNotFound:
		return errors.New(i18n.T("WebDAV 路径不存在（HTTP 404）"))
	case http.StatusInsufficientStorage:
		return errors.New(i18n.T("WebDAV 服务器空间不足"))
	}
	return fmt.Errorf(i18n.T("WebDAV 服务器返回 HTTP %d"), code)
}

// mkcol creates dir and its parents; existing directories are fine.
func (c *webdavClient) mkcol(ctx context.Context, dir string) error {
	segs := strings.Split(strings.Trim(dir, "/"), "/")
	cur := ""
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		cur = path.Join(cur, seg)
		// 201 created; 405 (and 301 from some servers) = already there.
		resp, err := c.do(ctx, "MKCOL", cur+"/", nil, nil, http.StatusCreated, http.StatusMethodNotAllowed, http.StatusMovedPermanently)
		if err != nil {
			return err
		}
		resp.Body.Close()
	}
	return nil
}

// put uploads data to p, replacing what was there.
func (c *webdavClient) put(ctx context.Context, p string, data []byte) error {
	resp, err := c.do(ctx, http.MethodPut, p, data, map[string]string{"Content-Type": "application/json"},
		http.StatusCreated, http.StatusNoContent, http.StatusOK)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// get downloads p, at most maxBackupBytes.
func (c *webdavClient) get(ctx context.Context, p string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, p, nil, nil, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBackupBytes+1))
	if err != nil {
		return nil, fmt.Errorf(i18n.T("下载备份失败：%v"), err)
	}
	if len(data) > maxBackupBytes {
		return nil, errors.New(i18n.T("备份文件过大，不像是澜流的设置文件。"))
	}
	return data, nil
}

// delete removes p; a file already gone is not an error.
func (c *webdavClient) delete(ctx context.Context, p string) error {
	resp, err := c.do(ctx, http.MethodDelete, p, nil, nil, http.StatusNoContent, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// davEntry is one member of a listed directory.
type davEntry struct {
	Name     string
	Dir      bool
	Size     int64
	Modified time.Time
}

// multistatus is the PROPFIND answer (RFC 4918 §9.1), namespaces ignored.
type multistatus struct {
	Responses []struct {
		Href     string `xml:"href"`
		Propstat []struct {
			Status string `xml:"status"`
			Prop   struct {
				Length   string `xml:"getcontentlength"`
				Modified string `xml:"getlastmodified"`
				Type     struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>
<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/></D:prop></D:propfind>`

// stat reports whether p exists (PROPFIND depth 0); a missing path is
// reported as ok=false without an error.
func (c *webdavClient) stat(ctx context.Context, p string) (bool, error) {
	resp, err := c.do(ctx, "PROPFIND", p, []byte(propfindBody), map[string]string{"Depth": "0", "Content-Type": "application/xml"},
		http.StatusMultiStatus, http.StatusOK, http.StatusNotFound)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode != http.StatusNotFound, nil
}

// list returns the members of dir (PROPFIND depth 1), the directory
// itself left out, sorted by name.
func (c *webdavClient) list(ctx context.Context, dir string) ([]davEntry, error) {
	resp, err := c.do(ctx, "PROPFIND", dir+"/", []byte(propfindBody), map[string]string{"Depth": "1", "Content-Type": "application/xml"},
		http.StatusMultiStatus, http.StatusOK)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf(i18n.T("读取目录列表失败：%v"), err)
	}
	return parseMultistatus(data, c.href(dir))
}

// parseMultistatus turns a PROPFIND answer into entries; self is the
// listed directory's URL, whose own entry is dropped.
func parseMultistatus(data []byte, self string) ([]davEntry, error) {
	var ms multistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, fmt.Errorf(i18n.T("目录列表不是有效的 WebDAV 应答：%v"), err)
	}
	selfPath := strings.TrimSuffix(hrefPath(self), "/")
	var out []davEntry
	for _, r := range ms.Responses {
		p := strings.TrimSuffix(hrefPath(strings.TrimSpace(r.Href)), "/")
		if p == "" || p == selfPath {
			continue
		}
		e := davEntry{Name: path.Base(p)}
		for _, ps := range r.Propstat {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
				continue
			}
			if ps.Prop.Type.Collection != nil {
				e.Dir = true
			}
			if n, err := strconv.ParseInt(strings.TrimSpace(ps.Prop.Length), 10, 64); err == nil {
				e.Size = n
			}
			if t, err := http.ParseTime(strings.TrimSpace(ps.Prop.Modified)); err == nil {
				e.Modified = t
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// hrefPath returns the unescaped path of an href, which servers give as
// an absolute path or a full URL.
func hrefPath(href string) string {
	if u, err := url.Parse(href); err == nil {
		if u.Path != "" {
			return u.Path
		}
		if p, err := url.PathUnescape(href); err == nil {
			return p
		}
	}
	return href
}
