package agent

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"lanflow/internal/capture/ca"
)

// Start is the first line the window writes to the agent's stdin.
type Start struct {
	// Op: "serve" (run), "ca-ensure" (load or create the root and
	// report it), "ca-regenerate" (always create a new root),
	// "ca-destroy" (delete the root's key).
	Op     string `json:"op"`
	CAPEM  string `json:"caPEM,omitempty"`
	Host   string `json:"host,omitempty"` // machine name shown in a new root's name
	Config Config `json:"config"`
	// CtlListen is the control API's address ("127.0.0.1:0").
	CtlListen string `json:"ctlListen,omitempty"`
	MemLimit  int64  `json:"memLimit,omitempty"` // GOMEMLIMIT of the agent
}

// Ready is the agent's one stdout line after Start.
type Ready struct {
	Error     string `json:"error,omitempty"`
	Listen    string `json:"listen,omitempty"`
	Ctl       string `json:"ctl,omitempty"`
	Token     string `json:"token,omitempty"`
	CAPEM     string `json:"caPEM,omitempty"`
	TrustLeaf string `json:"trustLeaf,omitempty"` // base64 DER of the trust-check leaf
	Created   bool   `json:"created,omitempty"`
}

// Main runs the agent subcommand: one Start line on stdin, one Ready
// line on stdout; a serving agent runs until stdin closes (the window
// quit or died) and then removes its spill file.
func Main(stdin io.Reader, stdout io.Writer, keys ca.KeyStore) int {
	br := bufio.NewReader(stdin)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return 2
	}
	var st Start
	if err := json.Unmarshal(line, &st); err != nil {
		writeReady(stdout, Ready{Error: "bad start: " + err.Error()})
		return 2
	}
	switch st.Op {
	case "ca-destroy":
		if st.CAPEM != "" {
			if c, err := ca.ParsePEM([]byte(st.CAPEM)); err == nil {
				if err := keys.Delete(ca.KeyName(c)); err != nil {
					writeReady(stdout, Ready{Error: err.Error()})
					return 1
				}
			}
		}
		writeReady(stdout, Ready{})
		return 0
	case "ca-ensure", "ca-regenerate":
		root, pemb, created, err := loadRoot(keys, st, st.Op == "ca-regenerate")
		if err != nil {
			writeReady(stdout, Ready{Error: err.Error()})
			return 1
		}
		writeReady(stdout, rootReady(root, pemb, created))
		return 0
	case "serve":
	default:
		writeReady(stdout, Ready{Error: "unknown op " + st.Op})
		return 2
	}
	if st.MemLimit > 0 {
		debug.SetMemoryLimit(st.MemLimit)
	} else {
		debug.SetMemoryLimit(256 << 20)
	}
	root, pemb, created, err := loadRoot(keys, st, false)
	if err != nil {
		writeReady(stdout, Ready{Error: err.Error()})
		return 1
	}
	CleanSpill(st.Config.SpillDir)
	a, err := New(st.Config, root)
	if err != nil {
		writeReady(stdout, Ready{Error: err.Error()})
		return 1
	}
	srv, err := NewControl(a, st.CtlListen)
	if err != nil {
		a.Close()
		writeReady(stdout, Ready{Error: err.Error()})
		return 1
	}
	r := rootReady(root, pemb, created)
	r.Listen, r.Ctl, r.Token = a.Addr(), srv.Addr(), srv.Token
	go a.Serve()
	go srv.Serve()
	writeReady(stdout, r)
	// run until stdin closes
	io.Copy(io.Discard, br)
	srv.Close()
	a.Close()
	return 0
}

func writeReady(w io.Writer, r Ready) {
	b, _ := json.Marshal(r)
	w.Write(append(b, '\n'))
}

func rootReady(root *ca.Root, pemb []byte, created bool) Ready {
	r := Ready{CAPEM: string(pemb), Created: created}
	if leaf, err := root.TrustLeaf(); err == nil {
		r.TrustLeaf = base64.StdEncoding.EncodeToString(leaf)
	}
	return r
}

// loadRoot opens the root of st.CAPEM, creating a new one when there is
// none, it cannot be opened, it expires within 7 days, or regen.
func loadRoot(keys ca.KeyStore, st Start, regen bool) (*ca.Root, []byte, bool, error) {
	if !regen && st.CAPEM != "" {
		r, err := ca.Load(keys, []byte(st.CAPEM))
		if err == nil && time.Until(r.Cert.NotAfter) > 7*24*time.Hour {
			return r, []byte(st.CAPEM), false, nil
		}
	}
	host := st.Host
	if host == "" {
		host, _ = os.Hostname()
	}
	r, pemb, err := ca.Generate(keys, host, time.Now())
	if err != nil {
		return nil, nil, false, fmt.Errorf("生成根证书失败：%w", err)
	}
	return r, pemb, true, nil
}

// ---- control API ----

// Control is the agent's control API: HTTP on loopback with a random
// bearer token.
type Control struct {
	a     *Agent
	ln    net.Listener
	srv   *http.Server
	Token string
}

// NewControl listens on addr ("" = 127.0.0.1:0).
func NewControl(a *Agent, addr string) (*Control, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tok := make([]byte, 32)
	rand.Read(tok)
	c := &Control{a: a, ln: ln, Token: hex.EncodeToString(tok)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", c.status)
	mux.HandleFunc("POST /config", c.config)
	mux.HandleFunc("GET /flows", c.flows)
	mux.HandleFunc("GET /flow", c.flow)
	mux.HandleFunc("POST /clear", c.clear)
	c.srv = &http.Server{Handler: c.auth(mux), ReadHeaderTimeout: 5 * time.Second}
	return c, nil
}

func (c *Control) Addr() string { return c.ln.Addr().String() }
func (c *Control) Serve() error { return c.srv.Serve(c.ln) }
func (c *Control) Close()       { c.srv.Close() }

func (c *Control) auth(h http.Handler) http.Handler {
	want := []byte("Bearer " + c.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Status is GET /status.
type Status struct {
	Enabled bool     `json:"enabled"`
	Flows   int      `json:"flows"`
	Accepts int64    `json:"accepts"`
	Pinned  []string `json:"pinned,omitempty"`
}

func (c *Control) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, Status{Enabled: c.a.Enabled(), Flows: c.a.Count(), Accepts: c.a.Accepts.Load(), Pinned: c.a.Pinned()})
}

func (c *Control) config(w http.ResponseWriter, r *http.Request) {
	var u Update
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&u); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.a.Apply(u)
	writeJSON(w, map[string]bool{"ok": true})
}

// FlowsResp is GET /flows.
type FlowsResp struct {
	Flows any   `json:"flows"`
	Seq   int64 `json:"seq"`
	MinID int64 `json:"minID"`
	Epoch int64 `json:"epoch"`
}

func (c *Control) flows(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	fs, seq, minID, ep := c.a.Flows(since, epoch)
	writeJSON(w, FlowsResp{Flows: fs, Seq: seq, MinID: minID, Epoch: ep})
}

func (c *Control) flow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	d, ok := c.a.Flow(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, d)
}

func (c *Control) clear(w http.ResponseWriter, r *http.Request) {
	c.a.Clear()
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ParseTrustLeaf decodes Ready.TrustLeaf.
func ParseTrustLeaf(s string) (*x509.Certificate, error) {
	if s == "" {
		return nil, errors.New("no trust leaf")
	}
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}
