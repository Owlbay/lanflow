package app

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/capture/agent"
	"lanflow/internal/capture/ca"
	"lanflow/internal/capture/har"
	"lanflow/internal/capture/trust"
	"lanflow/internal/fsx"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// HTTPS 选择性解密 (docs/capture.md). The window side: the selection and
// its file, the switch with its auto-off timer, the capture agent process
// (`lanflow capture-agent`, restarted when it dies), the root's trust and
// the records polled from the agent. The root's private key never enters
// this process: the agent creates and uses it.

// TopicCapture: CaptureStatus() / CaptureFlows() changed.
const TopicCapture Topic = "capture"

// CaptureAgentCommand returns the command that runs the agent; tests
// replace it (the default re-runs this executable as `capture-agent`).
var CaptureAgentCommand = func() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "capture-agent"), nil
}

// TrustHooks install and remove the root in the user's trust store and
// check it; tests replace them (never the real store).
var TrustHooks = struct {
	Install func(der []byte) error
	Remove  func(der []byte) error
	Trusted func(leaf *x509.Certificate) bool
}{trust.Install, trust.Remove, trust.Trusted}

const (
	captureDir          = "capture"
	captureSettingsFile = "settings.json"
	captureCAFile       = "ca.pem"
	captureLeafFile     = "trust-leaf.der"
	captureMaxFlows     = 5000
	captureRemoveAfter  = 7 * 24 * time.Hour
	captureExtend       = 30 * time.Minute
	capturePoll         = 700 * time.Millisecond
)

type captureState struct {
	opMu sync.Mutex // on/off, CA operations, agent restarts

	mu        sync.Mutex
	loaded    bool
	set       model.CaptureSettings
	enabled   bool
	until     time.Time
	run       *captureRun
	caPEM     []byte
	caCert    *x509.Certificate
	trustLeaf *x509.Certificate
	trusted   bool
	flows     []model.CaptureFlow // by ID
	index     map[int64]int
	seq       int64
	epoch     int64
	err       string
	restarts  []time.Time
	warns     []string
}

type captureRun struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	ctl       string
	token     string
	agentAddr string
	agentPort int
	upPort    int
	upPass    string
	outbound  string
	sels      []agent.Selector
	done      chan struct{}
	stopping  atomic.Bool
	http      *http.Client
}

func (s *Service) capDir() string { return filepath.Join(s.opt.DataDir, captureDir) }

// capLoad reads the settings and the root certificate once.
func (s *Service) capLoad() {
	c := &s.capst
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return
	}
	c.loaded = true
	c.set.AutoOffMin = -1
	if b, err := os.ReadFile(filepath.Join(s.capDir(), captureSettingsFile)); err == nil {
		var set model.CaptureSettings
		if json.Unmarshal(b, &set) == nil {
			c.set = set
		}
	}
	if b, err := os.ReadFile(filepath.Join(s.capDir(), captureCAFile)); err == nil {
		if cert, err := ca.ParsePEM(b); err == nil {
			c.caPEM, c.caCert = b, cert
		}
	}
	if b, err := os.ReadFile(filepath.Join(s.capDir(), captureLeafFile)); err == nil {
		c.trustLeaf, _ = x509.ParseCertificate(b)
	}
	c.trusted = c.caCert != nil && TrustHooks.Trusted(c.trustLeaf)
}

func (s *Service) capSave(set model.CaptureSettings) error {
	if err := os.MkdirAll(s.capDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(set, "", "  ")
	return fsx.WriteFile(filepath.Join(s.capDir(), captureSettingsFile), b, 0o600)
}

func autoOff(set model.CaptureSettings) time.Duration {
	switch {
	case set.AutoOffMin < 0:
		return model.CaptureDefaultAutoOff * time.Minute
	case set.AutoOffMin == 0:
		return 0
	}
	return time.Duration(set.AutoOffMin) * time.Minute
}

// CaptureBypass is the effective skip list: the built-in entries not
// turned off, then the user's.
func CaptureBypass(set model.CaptureSettings) []string {
	var out []string
	for _, b := range model.CaptureBuiltinBypass {
		if !slices.Contains(set.DisabledBuiltin, b) {
			out = append(out, b)
		}
	}
	return append(out, set.Bypass...)
}

// CaptureAppRule is the classical rule line of a program selection.
func CaptureAppRule(a model.CaptureApp) string {
	if a.Kind == "name" {
		return "PROCESS-NAME," + a.Match
	}
	return "PROCESS-PATH-WILDCARD," + a.Match
}

// captureInput is the compose input of the running capture (zero when off).
func (s *Service) captureInput(set model.Settings) profile.Capture {
	c := &s.capst
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.run
	if !c.enabled || r == nil {
		return profile.Capture{}
	}
	in := profile.Capture{AgentPort: r.agentPort, UpPort: r.upPort, UpUser: "lanflow", UpPass: r.upPass,
		Bypass: CaptureBypass(c.set), Ports: c.set.Ports, IPv6: set.Advanced.IPv6, Domains: c.set.Domains}
	for _, sel := range r.sels {
		cs := profile.CaptureSelector{Outbound: sel.Outbound, User: sel.User, Pass: sel.Pass}
		if sel.Kind == "domain" {
			in.Domain = cs
			continue
		}
		for _, a := range c.set.Apps {
			if a.Name == sel.Name {
				in.Apps = append(in.Apps, profile.CaptureApp{CaptureSelector: cs, Rules: []string{CaptureAppRule(a)}})
			}
		}
	}
	return in
}

func randToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func freeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// selectorsFor builds fresh credentials: one outbound for the domains,
// one per program.
func selectorsFor(set model.CaptureSettings, outbound string) []agent.Selector {
	sels := []agent.Selector{{User: "lanflow", Pass: randToken(16), Kind: "domain", Name: "", Outbound: outbound}}
	for i, a := range set.Apps {
		sels = append(sels, agent.Selector{User: fmt.Sprintf("app%d", i), Pass: randToken(16), Kind: "app", Name: a.Name,
			Outbound: fmt.Sprintf("%s-A%d", outbound, i)})
	}
	return sels
}

// agentStart runs the agent with op and the start line; for "serve" the
// process keeps running (stdin open), otherwise it exits after Ready.
func (s *Service) agentStart(st agent.Start) (*captureRun, agent.Ready, error) {
	cmd, err := CaptureAgentCommand()
	if err != nil {
		return nil, agent.Ready{}, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, agent.Ready{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, agent.Ready{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, agent.Ready{}, fmt.Errorf(i18n.T("无法启动解密进程：%v"), err)
	}
	line, _ := json.Marshal(st)
	stdin.Write(append(line, '\n'))
	type res struct {
		r   agent.Ready
		err error
	}
	ch := make(chan res, 1)
	go func() {
		br := bufio.NewReader(stdout)
		l, err := br.ReadBytes('\n')
		var r agent.Ready
		if err == nil {
			err = json.Unmarshal(l, &r)
		}
		ch <- res{r, err}
		io.Copy(io.Discard, br)
	}()
	var rd res
	select {
	case rd = <-ch:
	case <-time.After(60 * time.Second): // the keychain may ask the user first
		rd.err = errors.New("timeout")
	}
	if rd.err == nil && rd.r.Error != "" {
		rd.err = errors.New(rd.r.Error)
	}
	if rd.err != nil || st.Op != "serve" {
		stdin.Close()
		waited := make(chan struct{})
		go func() { cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-waited
		}
		if rd.err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg != "" {
				return nil, rd.r, fmt.Errorf(i18n.T("解密进程出错：%v（%s）"), rd.err, msg)
			}
			return nil, rd.r, fmt.Errorf(i18n.T("解密进程出错：%v"), rd.err)
		}
		return nil, rd.r, nil
	}
	r := &captureRun{cmd: cmd, stdin: stdin, ctl: rd.r.Ctl, token: rd.r.Token, agentAddr: rd.r.Listen, done: make(chan struct{}),
		http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}}
	if _, p, err := net.SplitHostPort(rd.r.Listen); err == nil {
		fmt.Sscan(p, &r.agentPort)
	}
	go func() { cmd.Wait(); close(r.done) }()
	return r, rd.r, nil
}

func (r *captureRun) stop() {
	r.stopping.Store(true)
	r.stdin.Close()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.cmd.Process.Kill()
		<-r.done
	}
}

func (r *captureRun) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+r.ctl+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errCaptureNotFound
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("agent %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

var errCaptureNotFound = i18n.Error("这条记录已不在抓包缓冲区中。")

// ---- reads ----

// CaptureStatus is a cheap read of the decryption state.
func (s *Service) CaptureStatus() model.CaptureStatus {
	s.capLoad()
	c := &s.capst
	s.mu.Lock()
	mode, core := s.state.settings.Mode, s.state.st.Core
	fpm := s.state.settings.Advanced.FindProcessMode
	s.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := model.CaptureStatus{Enabled: c.enabled, Domains: len(c.set.Domains), Apps: len(c.set.Apps), Until: c.until,
		AgentUp: c.run != nil, Flows: len(c.flows), Error: c.err, Settings: cloneCaptureSettings(c.set), FindProcess: true}
	if len(c.set.Apps) > 0 && strings.EqualFold(fpm, "off") {
		st.FindProcess = false
	}
	switch {
	case c.caCert == nil:
		st.CA = model.CANone
	case !c.trusted:
		st.CA = model.CAUntrusted
	case time.Until(c.caCert.NotAfter) < 7*24*time.Hour:
		st.CA = model.CAExpiring
	default:
		st.CA = model.CATrusted
	}
	if c.caCert != nil {
		st.CAExpires, st.CAName = c.caCert.NotAfter, c.caCert.Subject.CommonName
	}
	if c.enabled {
		switch {
		case core != model.CoreRunning:
			st.Paused, st.PauseReason = true, i18n.T("内核未运行")
		case mode != "" && mode != model.ModeRule:
			st.Paused, st.PauseReason = true, i18n.T("仅规则模式生效")
		}
	}
	return st
}

func cloneCaptureSettings(in model.CaptureSettings) model.CaptureSettings {
	out := in
	out.Domains = slices.Clone(in.Domains)
	out.Apps = slices.Clone(in.Apps)
	out.Bypass = slices.Clone(in.Bypass)
	out.DisabledBuiltin = slices.Clone(in.DisabledBuiltin)
	return out
}

// CaptureFlows is a cheap read of the records, oldest first.
func (s *Service) CaptureFlows() []model.CaptureFlow {
	c := &s.capst
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]model.CaptureFlow, len(c.flows))
	copy(out, c.flows)
	return out
}

// CaptureFlow returns one record with headers and bodies (from the agent).
func (s *Service) CaptureFlow(id int64) (model.CaptureFlowDetail, error) {
	c := &s.capst
	c.mu.Lock()
	r := c.run
	c.mu.Unlock()
	if r == nil {
		return model.CaptureFlowDetail{}, i18n.Error("解密已关闭，记录已清空。")
	}
	var d model.CaptureFlowDetail
	if err := r.call("GET", fmt.Sprintf("/flow?id=%d", id), nil, &d); err != nil {
		return d, err
	}
	return d, nil
}

// ---- switch ----

// SetCapture turns decryption on or off (docs/capture.md §2 关键约束).
func (s *Service) SetCapture(on bool) (err error) {
	defer func() { s.logOp(fmt.Sprintf("capture on=%v", on), err) }()
	s.capLoad()
	c := &s.capst
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if on {
		return s.captureOnLocked()
	}
	return s.captureOffLocked(true)
}

func (s *Service) captureOnLocked() error {
	c := &s.capst
	c.mu.Lock()
	set := cloneCaptureSettings(c.set)
	enabled, caPEM, trusted, caCert := c.enabled, c.caPEM, c.trusted, c.caCert
	c.mu.Unlock()
	if enabled {
		return nil
	}
	if !set.Acknowledged {
		return i18n.Error("首次开启解密前，请先在「抓包」页阅读说明并勾选确认。")
	}
	if len(set.Domains) == 0 && len(set.Apps) == 0 {
		return i18n.Error("还没有选择要解密的域名或程序。")
	}
	if caCert == nil || !trusted {
		return i18n.Error("根证书尚未安装或系统尚未信任，请先在「抓包」页安装根证书。")
	}
	if time.Until(caCert.NotAfter) < 7*24*time.Hour {
		return i18n.Error("根证书即将过期，请先在「抓包」页重新生成根证书。")
	}
	if err := s.startAgentLocked(set, caPEM, 0); err != nil {
		return err
	}
	c.mu.Lock()
	c.enabled, c.err = true, ""
	if d := autoOff(set); d > 0 {
		c.until = time.Now().Add(d)
	} else {
		c.until = time.Time{}
	}
	c.mu.Unlock()
	if err := s.captureReload(); err != nil {
		s.captureOffLocked(false)
		return err
	}
	s.em.emit(TopicCapture, TopicStatus)
	return nil
}

// startAgentLocked starts the agent; port > 0 asks for the same agent
// port (a restart keeps the running config valid).
func (s *Service) startAgentLocked(set model.CaptureSettings, caPEM []byte, port int) error {
	c := &s.capst
	c.mu.Lock()
	prev := c.run
	c.mu.Unlock()
	var outbound, upPass string
	var upPort int
	var sels []agent.Selector
	if prev != nil {
		outbound, upPort, upPass, sels = prev.outbound, prev.upPort, prev.upPass, prev.sels
	} else {
		outbound = profile.CaptureOutboundPrefix + "-" + randToken(8)
		upPass = randToken(16)
		p, err := freeLoopbackPort()
		if err != nil {
			return err
		}
		upPort = p
		sels = selectorsFor(set, outbound)
	}
	listen := "127.0.0.1:0"
	if port > 0 {
		listen = fmt.Sprintf("127.0.0.1:%d", port)
	}
	ctrlAddr := s.ctrlAddr
	st := agent.Start{Op: "serve", CAPEM: string(caPEM), Config: agent.Config{
		Listen: listen, Selectors: sels, UpAddr: fmt.Sprintf("127.0.0.1:%d", upPort), UpUser: "lanflow", UpPass: upPass,
		Domains: set.Domains, Bypass: CaptureBypass(set), Enabled: true, Controller: ctrlAddr,
		SpillDir: filepath.Join(s.capDir(), "spill"), MaxFlows: captureMaxFlows,
	}}
	r, ready, err := s.agentStart(st)
	if err != nil {
		return err
	}
	if ready.Created {
		// the agent could not open the stored key and made a new root: it is
		// not trusted, so nothing would decrypt
		r.stop()
		s.capUpdateCA([]byte(ready.CAPEM), ready.TrustLeaf)
		return i18n.Error("根证书的私钥已丢失，已生成新的根证书；请在「抓包」页重新安装后再开启。")
	}
	r.outbound, r.upPort, r.upPass, r.sels = outbound, upPort, upPass, sels
	c.mu.Lock()
	c.run = r
	c.flows, c.index, c.seq, c.epoch = nil, map[int64]int{}, 0, 0
	c.mu.Unlock()
	s.goBackground(func() { s.captureWatch(r) })
	return nil
}

// captureOffLocked: agent to passthrough → reload without the layer →
// close the connections through the outbounds → stop the agent.
func (s *Service) captureOffLocked(record bool) error {
	c := &s.capst
	c.mu.Lock()
	r := c.run
	was := c.enabled
	c.enabled, c.until = false, time.Time{}
	c.mu.Unlock()
	if r != nil {
		off := false
		r.call("POST", "/config", agent.Update{Enabled: &off}, nil)
	}
	var rerr error
	if was {
		rerr = s.captureReload()
	}
	if r != nil {
		s.closeCaptureConns(r)
		c.mu.Lock()
		c.run = nil
		c.mu.Unlock()
		r.stop()
	}
	if record && was {
		c.mu.Lock()
		c.set.LastDisabled = time.Now()
		set := cloneCaptureSettings(c.set)
		c.mu.Unlock()
		s.capSave(set)
	}
	s.em.emit(TopicCapture, TopicStatus)
	return rerr
}

// captureReload recomposes and hot-reloads the core (when it runs).
func (s *Service) captureReload() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reloadLocked()
}

// closeCaptureConns closes the core's connections through a decrypting
// outbound (a reload keeps them, docs/capture.md §2).
func (s *Service) closeCaptureConns(r *captureRun) {
	if !s.running() {
		return
	}
	ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
	defer cancel()
	cs, err := s.d.ctrl.Connections(ctx)
	if err != nil {
		return
	}
	for _, cn := range cs.Connections {
		for _, ch := range cn.Chains {
			if strings.HasPrefix(ch, profile.CaptureOutboundPrefix) {
				s.d.ctrl.CloseConnection(ctx, cn.ID)
				break
			}
		}
	}
}

// ExtendCapture pushes the auto-off time out by 30 minutes.
func (s *Service) ExtendCapture() error {
	c := &s.capst
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return i18n.Error("解密未开启。")
	}
	if !c.until.IsZero() {
		base := c.until
		if time.Now().After(base) {
			base = time.Now()
		}
		c.until = base.Add(captureExtend)
	}
	go s.em.emit(TopicCapture)
	return nil
}

// ---- selection ----

// SaveCaptureSettings stores the selection; a running capture follows it.
func (s *Service) SaveCaptureSettings(set model.CaptureSettings) error {
	s.capLoad()
	for _, d := range set.Domains {
		if err := checkCapturePattern(d); err != nil {
			return err
		}
	}
	for _, a := range set.Apps {
		if strings.TrimSpace(a.Match) == "" || strings.TrimSpace(a.Name) == "" {
			return i18n.Error("程序选择项需要名称和匹配路径。")
		}
	}
	c := &s.capst
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	prev := c.set
	set.LastDisabled = prev.LastDisabled
	c.set = cloneCaptureSettings(set)
	enabled, r := c.enabled, c.run
	c.mu.Unlock()
	if err := s.capSave(set); err != nil {
		return err
	}
	if enabled && r != nil {
		if len(set.Domains) == 0 && len(set.Apps) == 0 {
			return s.captureOffLocked(true)
		}
		if !slices.Equal(appNames(prev.Apps), appNames(set.Apps)) {
			// new program credentials: restart the agent with new selectors
			c.mu.Lock()
			c.run = nil
			c.mu.Unlock()
			r.stop()
			if err := s.startAgentLocked(set, c.caPEM, 0); err != nil {
				s.captureOffLocked(true)
				return err
			}
		} else {
			doms, byp := set.Domains, CaptureBypass(set)
			r.call("POST", "/config", agent.Update{Domains: &doms, Bypass: &byp}, nil)
		}
		if err := s.captureReload(); err != nil {
			return err
		}
	}
	s.em.emit(TopicCapture)
	return nil
}

func appNames(as []model.CaptureApp) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Name+"\x00"+a.Kind+"\x00"+a.Match)
	}
	return out
}

func checkCapturePattern(p string) error {
	p = strings.TrimSpace(p)
	h := strings.TrimPrefix(strings.TrimPrefix(p, "+."), "*.")
	if h == "" || strings.ContainsAny(h, " /,:") || !strings.Contains(h, ".") {
		return fmt.Errorf(i18n.T("「%s」不是有效的域名（写法：example.com、*.example.com、+.example.com）"), p)
	}
	if net.ParseIP(h) != nil {
		return fmt.Errorf(i18n.T("「%s」是 IP 地址：根证书不为 IP 签发证书，只能按域名解密"), p)
	}
	return nil
}

// AddCaptureDomain adds host to the selection (连接页 · 解密此主机…) and,
// with enable, turns decryption on.
func (s *Service) AddCaptureDomain(host string, enable bool) error {
	s.capLoad()
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if err := checkCapturePattern(host); err != nil {
		return err
	}
	c := &s.capst
	c.mu.Lock()
	set := cloneCaptureSettings(c.set)
	c.mu.Unlock()
	if !slices.Contains(set.Domains, host) {
		set.Domains = append(set.Domains, host)
	}
	if err := s.SaveCaptureSettings(set); err != nil {
		return err
	}
	if enable {
		return s.SetCapture(true)
	}
	return nil
}

// AddCaptureApp adds a program (连接页 · 解密此程序…): on macOS the
// bundle's path with /* so helper processes are covered.
func (s *Service) AddCaptureApp(processPath, process string, enable bool) error {
	s.capLoad()
	app := CaptureAppFor(processPath, process)
	if app.Match == "" {
		return i18n.Error("这个连接没有进程信息，无法按程序解密（需要 find-process-mode 不为 off）。")
	}
	c := &s.capst
	c.mu.Lock()
	set := cloneCaptureSettings(c.set)
	c.mu.Unlock()
	for _, a := range set.Apps {
		if a.Match == app.Match && a.Kind == app.Kind {
			app.Name = ""
		}
	}
	if app.Name != "" {
		set.Apps = append(set.Apps, app)
	}
	if err := s.SaveCaptureSettings(set); err != nil {
		return err
	}
	if enable {
		return s.SetCapture(true)
	}
	return nil
}

// CaptureAppFor turns a connection's process into a program selection: a
// macOS app bundle becomes "<bundle>.app/*" (helpers included), any other
// path matches exactly, a bare name by name.
func CaptureAppFor(path, name string) model.CaptureApp {
	if path != "" {
		if i := strings.Index(path, ".app/"); i > 0 {
			bundle := path[:i+4]
			return model.CaptureApp{Name: strings.TrimSuffix(filepath.Base(bundle), ".app"), Kind: "path", Match: bundle + "/*"}
		}
		return model.CaptureApp{Name: filepath.Base(path), Kind: "path", Match: path}
	}
	if name != "" {
		return model.CaptureApp{Name: name, Kind: "name", Match: name}
	}
	return model.CaptureApp{}
}

// ClearCaptureFlows forgets the records (the agent's too).
func (s *Service) ClearCaptureFlows() error {
	c := &s.capst
	c.mu.Lock()
	r := c.run
	c.flows, c.index = nil, map[int64]int{}
	c.mu.Unlock()
	if r != nil {
		r.call("POST", "/clear", nil, nil)
	}
	s.em.emit(TopicCapture)
	return nil
}

// ---- root certificate ----

func (s *Service) capUpdateCA(pemb []byte, leafB64 string) {
	c := &s.capst
	cert, err := ca.ParsePEM(pemb)
	if err != nil {
		return
	}
	leaf, _ := agent.ParseTrustLeaf(leafB64)
	os.MkdirAll(s.capDir(), 0o700)
	fsx.WriteFile(filepath.Join(s.capDir(), captureCAFile), pemb, 0o600)
	if leaf != nil {
		fsx.WriteFile(filepath.Join(s.capDir(), captureLeafFile), leaf.Raw, 0o600)
	}
	c.mu.Lock()
	c.caPEM, c.caCert, c.trustLeaf = pemb, cert, leaf
	c.trusted = TrustHooks.Trusted(leaf)
	c.mu.Unlock()
}

// InstallCACert creates the root when there is none (or it nears expiry)
// and asks the system to trust it.
func (s *Service) InstallCACert() (err error) {
	defer func() { s.logOp("capture install root", err) }()
	s.capLoad()
	c := &s.capst
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	pemb := c.caPEM
	c.mu.Unlock()
	host, _ := os.Hostname()
	_, ready, err := s.agentStart(agent.Start{Op: "ca-ensure", CAPEM: string(pemb), Host: host})
	if err != nil {
		return err
	}
	old := pemb
	s.capUpdateCA([]byte(ready.CAPEM), ready.TrustLeaf)
	c.mu.Lock()
	cert, trusted := c.caCert, c.trusted
	c.mu.Unlock()
	if !trusted {
		if err := TrustHooks.Install(cert.Raw); err != nil {
			s.em.emit(TopicCapture)
			return fmt.Errorf(i18n.T("安装根证书失败：%v"), err)
		}
	}
	if ready.Created && old != nil {
		s.dropOldRoot(old)
	}
	c.mu.Lock()
	c.trusted = TrustHooks.Trusted(c.trustLeaf)
	trusted = c.trusted
	c.mu.Unlock()
	s.em.emit(TopicCapture)
	if !trusted {
		return i18n.Error("已请求安装根证书，但系统仍不信任它（可能被取消，或被设备管理策略禁止）。")
	}
	return nil
}

// dropOldRoot removes an old root's trust and key.
func (s *Service) dropOldRoot(pemb []byte) {
	if cert, err := ca.ParsePEM(pemb); err == nil {
		TrustHooks.Remove(cert.Raw)
	}
	s.agentStart(agent.Start{Op: "ca-destroy", CAPEM: string(pemb)})
}

// RegenerateCA makes a new root, trusts it, removes the old one and
// restarts decryption when it was on (docs/capture.md §6 重新生成).
func (s *Service) RegenerateCA() (err error) {
	defer func() { s.logOp("capture regenerate root", err) }()
	s.capLoad()
	c := &s.capst
	c.opMu.Lock()
	c.mu.Lock()
	was, old := c.enabled, c.caPEM
	c.mu.Unlock()
	if was {
		s.captureOffLocked(false)
	}
	host, _ := os.Hostname()
	_, ready, err := s.agentStart(agent.Start{Op: "ca-regenerate", Host: host})
	if err != nil {
		c.opMu.Unlock()
		return err
	}
	s.capUpdateCA([]byte(ready.CAPEM), ready.TrustLeaf)
	c.mu.Lock()
	cert := c.caCert
	c.mu.Unlock()
	ierr := TrustHooks.Install(cert.Raw)
	if old != nil {
		s.dropOldRoot(old)
	}
	c.mu.Lock()
	c.trusted = TrustHooks.Trusted(c.trustLeaf)
	c.mu.Unlock()
	c.opMu.Unlock()
	s.em.emit(TopicCapture)
	if ierr != nil {
		return fmt.Errorf(i18n.T("安装根证书失败：%v"), ierr)
	}
	if was {
		return s.SetCapture(true)
	}
	return nil
}

// RemoveCACert turns decryption off, removes the root's trust and
// deletes its key and certificate.
func (s *Service) RemoveCACert() (err error) {
	defer func() { s.logOp("capture remove root", err) }()
	s.capLoad()
	c := &s.capst
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return s.removeCALocked()
}

func (s *Service) removeCALocked() error {
	c := &s.capst
	s.captureOffLocked(true)
	c.mu.Lock()
	pemb, cert := c.caPEM, c.caCert
	c.mu.Unlock()
	if cert == nil {
		return nil
	}
	if err := TrustHooks.Remove(cert.Raw); err != nil {
		return fmt.Errorf(i18n.T("移除根证书失败：%v"), err)
	}
	if _, _, err := s.agentStart(agent.Start{Op: "ca-destroy", CAPEM: string(pemb)}); err != nil {
		return err
	}
	for _, f := range []string{captureCAFile, captureLeafFile} {
		os.Remove(filepath.Join(s.capDir(), f))
	}
	c.mu.Lock()
	c.caPEM, c.caCert, c.trustLeaf, c.trusted = nil, nil, nil, false
	c.mu.Unlock()
	s.em.emit(TopicCapture)
	return nil
}

// ExportCACert writes the root certificate (never the key) as PEM.
func (s *Service) ExportCACert(path string) error {
	s.capLoad()
	c := &s.capst
	c.mu.Lock()
	pemb := c.caPEM
	c.mu.Unlock()
	if pemb == nil {
		return i18n.Error("还没有根证书。")
	}
	return fsx.WriteFile(path, pemb, 0o644)
}

// ---- background ----

// captureLoop runs for the service's life: the auto-off timer (by wall
// clock, so a sleep past the deadline turns off at wake), polling the
// agent's records, the trust re-check and the 7-day removal.
func (s *Service) captureLoop() {
	s.capLoad()
	s.captureAutoRemove()
	poll := time.NewTicker(capturePoll)
	defer poll.Stop()
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			s.captureShutdown()
			return
		case <-hourly.C:
			s.captureAutoRemove()
			s.capRecheckTrust()
		case <-poll.C:
			s.captureTick()
		}
	}
}

// captureTick turns decryption off once its time is up and pulls new
// records from the agent.
func (s *Service) captureTick() {
	c := &s.capst
	c.mu.Lock()
	due := c.enabled && !c.until.IsZero() && time.Now().After(c.until)
	r := c.run
	c.mu.Unlock()
	if due {
		s.SetCapture(false)
		s.notify(Notice{Kind: "toast", Title: i18n.T("HTTPS 解密已自动关闭")})
		return
	}
	if r != nil {
		s.capturePoll(r)
	}
}

func (s *Service) capRecheckTrust() {
	c := &s.capst
	c.mu.Lock()
	leaf, had := c.trustLeaf, c.trusted
	c.mu.Unlock()
	now := c.caCert != nil && TrustHooks.Trusted(leaf)
	if now != had {
		c.mu.Lock()
		c.trusted = now
		c.mu.Unlock()
		s.em.emit(TopicCapture)
	}
}

// captureAutoRemove removes trust and key seven days after the last
// time decryption was turned off (docs/capture.md §6 自动移除).
func (s *Service) captureAutoRemove() {
	c := &s.capst
	c.mu.Lock()
	due := !c.enabled && c.caCert != nil && !c.set.LastDisabled.IsZero() && time.Since(c.set.LastDisabled) > captureRemoveAfter
	expired := !c.enabled && c.caCert != nil && time.Now().After(c.caCert.NotAfter)
	c.mu.Unlock()
	if !due && !expired {
		return
	}
	if err := s.RemoveCACert(); err == nil {
		s.notify(Notice{Kind: "toast", Title: i18n.T("已移除抓包根证书"), Detail: i18n.T("解密关闭超过 7 天（或根证书已过期），根证书的信任和私钥已自动移除；下次开启时会重新生成。")})
	}
}

func (s *Service) capturePoll(r *captureRun) {
	c := &s.capst
	c.mu.Lock()
	since, epoch := c.seq, c.epoch
	c.mu.Unlock()
	var resp struct {
		Flows []model.CaptureFlow `json:"flows"`
		Seq   int64               `json:"seq"`
		MinID int64               `json:"minID"`
		Epoch int64               `json:"epoch"`
	}
	if err := r.call("GET", fmt.Sprintf("/flows?since=%d&epoch=%d", since, epoch), nil, &resp); err != nil {
		return
	}
	if resp.Seq == since && resp.Epoch == epoch {
		return
	}
	c.mu.Lock()
	if c.run != r {
		c.mu.Unlock()
		return
	}
	if resp.Epoch != c.epoch {
		c.flows, c.index = nil, map[int64]int{}
	}
	if c.index == nil {
		c.index = map[int64]int{}
	}
	for _, f := range resp.Flows {
		if i, ok := c.index[f.ID]; ok {
			c.flows[i] = f
			continue
		}
		c.flows = append(c.flows, f)
		c.index[f.ID] = len(c.flows) - 1
	}
	if len(c.flows) > 0 && c.flows[0].ID < resp.MinID {
		k := sort.Search(len(c.flows), func(i int) bool { return c.flows[i].ID >= resp.MinID })
		c.flows = append([]model.CaptureFlow(nil), c.flows[k:]...)
		c.index = make(map[int64]int, len(c.flows))
		for i, f := range c.flows {
			c.index[f.ID] = i
		}
	}
	c.seq, c.epoch = resp.Seq, resp.Epoch
	c.mu.Unlock()
	s.em.emit(TopicCapture)
}

// captureWatch restarts an agent that died while decryption is on, on
// the same port so the running config stays valid; three deaths within a
// minute turn decryption off (docs/capture.md §5.1 崩溃).
func (s *Service) captureWatch(r *captureRun) {
	select {
	case <-r.done:
	case <-s.rootCtx.Done():
		return
	}
	if r.stopping.Load() || s.rootCtx.Err() != nil {
		return
	}
	c := &s.capst
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	if c.run != r || !c.enabled {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	var recent []time.Time
	for _, t := range c.restarts {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	c.restarts = append(recent, now)
	tooMany := len(c.restarts) > 3
	set, caPEM := cloneCaptureSettings(c.set), c.caPEM
	c.mu.Unlock()
	s.alog.Errorf("capture agent exited unexpectedly")
	if !tooMany {
		if err := s.startAgentLocked(set, caPEM, r.agentPort); err == nil {
			c.mu.Lock()
			nr := c.run
			c.mu.Unlock()
			if nr.agentPort != r.agentPort {
				s.captureReload()
			}
			s.em.emit(TopicCapture)
			return
		}
	}
	c.mu.Lock()
	c.run = nil
	c.err = i18n.T("解密进程多次意外退出，已关闭解密。")
	c.mu.Unlock()
	s.captureOffLocked(true)
	s.notify(Notice{Kind: "error", Title: i18n.T("HTTPS 解密已关闭"), Detail: i18n.T("解密进程多次意外退出，已关闭解密并恢复原有分流。")})
}

func (s *Service) captureShutdown() {
	c := &s.capst
	c.mu.Lock()
	r := c.run
	c.run, c.enabled = nil, false
	c.mu.Unlock()
	if r != nil {
		r.stop()
	}
}

// captureAfterReload keeps GLOBAL off the decrypting outbound (it cannot
// be excluded from GLOBAL, docs/capture.md §3.3).
func (s *Service) captureAfterReload() {
	s.mu.Lock()
	var now string
	for _, g := range s.state.proxies.Groups {
		if g.Name == "GLOBAL" {
			now = g.Now
		}
	}
	s.mu.Unlock()
	if strings.HasPrefix(now, profile.CaptureOutboundPrefix) {
		ctx, cancel := s.ctrlCtx(nil, ctrlTimeout)
		s.d.ctrl.SelectProxy(ctx, "GLOBAL", "DIRECT")
		cancel()
		s.refreshProxies()
	}
}

// hideCaptureProxies removes the decrypting outbounds from a snapshot.
func hideCaptureProxies(p *model.ProxySnapshot) {
	for k := range p.Proxies {
		if strings.HasPrefix(k, profile.CaptureOutboundPrefix) {
			delete(p.Proxies, k)
		}
	}
	for i := range p.Groups {
		p.Groups[i].All = slices.DeleteFunc(p.Groups[i].All, func(n string) bool { return strings.HasPrefix(n, profile.CaptureOutboundPrefix) })
	}
}

// ExportHAR writes the decrypted records as HAR (har.Build).
func (s *Service) ExportHAR(path string, withBodies bool) error {
	c := &s.capst
	c.mu.Lock()
	r := c.run
	flows := slices.Clone(c.flows)
	c.mu.Unlock()
	if r == nil {
		return i18n.Error("解密已关闭，没有可导出的记录。")
	}
	var ds []model.CaptureFlowDetail
	for _, f := range flows {
		if f.Method == "" || !f.Has(model.FlowDecrypted) {
			continue
		}
		var d model.CaptureFlowDetail
		if err := r.call("GET", fmt.Sprintf("/flow?id=%d", f.ID), nil, &d); err == nil {
			if !withBodies {
				d.ReqBody, d.RespBody = nil, nil
			}
			ds = append(ds, d)
		}
	}
	b, err := har.Build(ds, withBodies, s.opt.Version)
	if err != nil {
		return err
	}
	return fsx.WriteFile(path, b, 0o600)
}

// ---- diagnostics ----

// redactCaptureLayer replaces the payload of the capture rule sets (the
// selected domains and program paths) by a count (docs/capture.md §8).
func redactCaptureLayer(doc *yaml.Node) {
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "rule-providers" || root.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		rp := root.Content[i+1]
		for j := 0; j+1 < len(rp.Content); j += 2 {
			if !strings.HasPrefix(rp.Content[j].Value, "lanflow-mitm-") || rp.Content[j+1].Kind != yaml.MappingNode {
				continue
			}
			m := rp.Content[j+1]
			for k := 0; k+1 < len(m.Content); k += 2 {
				if m.Content[k].Value == "payload" {
					n := len(m.Content[k+1].Content)
					m.Content[k+1] = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq",
						Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprintf("(%d 项，已隐藏)", n)}}}
				}
			}
		}
	}
}

// dropCaptureLines removes the core's log lines about decrypted
// connections (they name the hosts: "… using LANFLOW-MITM-…").
func dropCaptureLines(data []byte) []byte {
	if !bytes.Contains(data, []byte("LANFLOW-MITM")) && !bytes.Contains(data, []byte("lanflow-mitm-")) {
		return data
	}
	var out []byte
	for _, l := range bytes.SplitAfter(data, []byte("\n")) {
		if bytes.Contains(l, []byte("LANFLOW-MITM")) || bytes.Contains(l, []byte("lanflow-mitm-")) {
			continue
		}
		out = append(out, l...)
	}
	return out
}
