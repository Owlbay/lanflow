// Package ctl is `lanflow ctl`: commands that call a running Lanflow
// through the local interface (internal/rpc, docs/service-split.md §5).
// Output is JSON by default; --format table is for people.
package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/remote"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
)

// Exit codes.
const (
	ExitOK         = 0
	ExitError      = 1 // the command failed
	ExitUsage      = 2
	ExitNotRunning = 3 // Lanflow is not running
)

// Env is what a run needs from outside.
type Env struct {
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	Version        string
	// Dial connects to Lanflow; nil dials the default endpoint.
	Dial func(ctx context.Context, reveal bool) (*remote.Client, error)
}

// DefaultDial connects to the Lanflow of the current user.
func DefaultDial(version string) func(ctx context.Context, reveal bool) (*remote.Client, error) {
	return func(ctx context.Context, reveal bool) (*remote.Client, error) {
		return remote.Dial(ctx, remote.Options{DataDir: app.DefaultOptions("Lanflow").DataDir, Client: rpc.KindCtl, Version: version, Reveal: reveal})
	}
}

const usage = `用法: lanflow ctl <命令> [参数] [--format json|table]

命令:
  status                              内核、系统代理、TUN、模式与当前订阅
  proxies list [代理组]                代理组、当前选择与节点延迟
  proxies select <代理组> <节点>        切换节点
  proxies test <代理组>                 测试代理组内节点的延迟
  mode set <rule|global|direct>       切换出站模式
  sub list                            订阅列表（不含订阅链接）
  sub add <链接> [--name 名称] [--use]  导入订阅
  sub use <ID或名称>                    切换到某个订阅
  sub update [ID或名称|--all]           更新订阅（默认当前订阅）
  rule add <类型> <内容> <目标> [--dry-run]  添加自定义规则，如 DOMAIN-SUFFIX example.com 节点选择
  rule test <域名或IP>                  查看会命中哪条规则、走哪个出站
  config get [字段路径]                 读取设置（已脱敏），如 config get DNS.TakeOver
  config set <字段路径> <值> [--dry-run] 修改一项设置，值按 JSON 解析，否则视为字符串
  config apply <文件|-> [--dry-run]      按 JSON 补丁修改设置；--dry-run 只输出差异
  diag [--run]                        诊断摘要；--run 先重新测试网站连通性和泄漏
  undo                                撤销 ctl/MCP 最近一次配置写入
  audit [条数]                         最近的审计记录
  methods                             本机接口的方法清单与权限级别
  call <方法> [JSON 参数...]            直接调用本机接口的方法

选项:
  --format json|table   输出格式，默认 json
  --reveal              允许敏感读取（需要在澜流界面中确认）
  --timeout 秒数         单次调用超时，默认 120
`

type opts struct {
	format  string
	reveal  bool
	dryRun  bool
	all     bool
	use     bool
	run     bool
	name    string
	timeout time.Duration
	args    []string
}

func parseArgs(args []string) (opts, error) {
	o := opts{format: "json", timeout: 120 * time.Second}
	for i := 0; i < len(args); i++ {
		a := args[i]
		val := func() (string, error) {
			if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(k, "--") {
				return v, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 需要一个值", a)
			}
			i++
			return args[i], nil
		}
		key, _, _ := strings.Cut(a, "=")
		switch key {
		case "--format":
			v, err := val()
			if err != nil {
				return o, err
			}
			if v != "json" && v != "table" {
				return o, fmt.Errorf("--format 只能是 json 或 table")
			}
			o.format = v
		case "--name":
			v, err := val()
			if err != nil {
				return o, err
			}
			o.name = v
		case "--timeout":
			v, err := val()
			if err != nil {
				return o, err
			}
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return o, fmt.Errorf("--timeout 应为正整数（秒）")
			}
			o.timeout = time.Duration(n) * time.Second
		case "--reveal":
			o.reveal = true
		case "--dry-run":
			o.dryRun = true
		case "--all":
			o.all = true
		case "--use":
			o.use = true
		case "--run":
			o.run = true
		case "-h", "--help":
			o.args = append([]string{"help"}, o.args...)
		default:
			if strings.HasPrefix(a, "--") {
				return o, fmt.Errorf("未知选项 %s", a)
			}
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

// usageError is a wrong command line.
type usageError string

func (e usageError) Error() string { return string(e) }

// Main runs `lanflow ctl args...` and returns the exit code.
func Main(args []string, env Env) int {
	if env.Stdout == nil {
		env.Stdout = os.Stdout
	}
	if env.Stderr == nil {
		env.Stderr = os.Stderr
	}
	if env.Stdin == nil {
		env.Stdin = os.Stdin
	}
	if env.Dial == nil {
		env.Dial = DefaultDial(env.Version)
	}
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lanflow ctl: %v\n\n%s", err, usage)
		return ExitUsage
	}
	if len(o.args) == 0 || o.args[0] == "help" {
		fmt.Fprint(env.Stdout, usage)
		if len(o.args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	ctx := context.Background()
	c, err := env.Dial(ctx, o.reveal)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lanflow ctl: %v\n", err)
		if errors.Is(err, rpc.ErrNotRunning) {
			return ExitNotRunning
		}
		return ExitError
	}
	defer c.Close()
	c.Timeout = o.timeout
	r := &runner{c: c, o: o, env: env}
	out, err := r.run()
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(env.Stderr, "lanflow ctl: %v\n\n%s", err, usage)
			return ExitUsage
		}
		fmt.Fprintf(env.Stderr, "lanflow ctl: %v\n", err)
		return ExitError
	}
	if out != nil {
		if err := r.print(out); err != nil {
			fmt.Fprintf(env.Stderr, "lanflow ctl: %v\n", err)
			return ExitError
		}
	}
	return ExitOK
}

type runner struct {
	c   *remote.Client
	o   opts
	env Env
}

func (r *runner) need(n int, what string) error {
	if len(r.o.args) < n {
		return usageError("缺少参数：" + what)
	}
	return nil
}

func (r *runner) arg(i int) string {
	if i < len(r.o.args) {
		return r.o.args[i]
	}
	return ""
}

// read checks a read's error (reads return zero values on failure).
func (r *runner) read() error { return r.c.LastError() }

func (r *runner) run() (any, error) {
	cmd, sub := r.arg(0), r.arg(1)
	switch cmd {
	case "status":
		st := r.c.Status()
		if err := r.read(); err != nil {
			return nil, err
		}
		return statusView(st, r.c.Subscriptions(), r.c.Proxies()), r.read()
	case "proxies":
		switch sub {
		case "list", "":
			return r.proxiesList(r.arg(2))
		case "select":
			if err := r.need(4, "proxies select <代理组> <节点>"); err != nil {
				return nil, err
			}
			if err := r.c.SelectProxy(r.arg(2), r.arg(3)); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "group": r.arg(2), "selected": r.arg(3)}, nil
		case "test":
			if err := r.need(3, "proxies test <代理组>"); err != nil {
				return nil, err
			}
			if err := r.c.TestGroupDelay(r.arg(2)); err != nil {
				return nil, err
			}
			return r.proxiesList(r.arg(2))
		}
	case "mode":
		if sub == "set" {
			if err := r.need(3, "mode set <rule|global|direct>"); err != nil {
				return nil, err
			}
			m := model.Mode(strings.ToLower(r.arg(2)))
			if m != model.ModeRule && m != model.ModeGlobal && m != model.ModeDirect {
				return nil, usageError("模式只能是 rule、global 或 direct")
			}
			if err := r.c.SetMode(m); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "mode": m}, nil
		}
	case "sub":
		return r.subCmd(sub)
	case "rule":
		return r.ruleCmd(sub)
	case "config":
		return r.configCmd(sub)
	case "diag":
		return Diagnose(r.c, r.o.run)
	case "undo":
		var u rpc.UndoResult
		if err := r.c.Conn().CallAPI(context.Background(), "Undo", nil, &u); err != nil {
			return nil, err
		}
		return u, nil
	case "audit":
		n := 20
		if sub != "" {
			v, err := strconv.Atoi(sub)
			if err != nil {
				return nil, usageError("条数应为整数")
			}
			n = v
		}
		var es []rpc.AuditEntry
		if err := r.c.Conn().CallAPI(context.Background(), "AuditLog", []any{n}, &es); err != nil {
			return nil, err
		}
		return es, nil
	case "methods":
		var ms []rpc.Method
		if err := r.c.Conn().CallAPI(context.Background(), "Methods", nil, &ms); err != nil {
			return nil, err
		}
		return ms, nil
	case "call":
		if err := r.need(2, "call <方法> [JSON 参数...]"); err != nil {
			return nil, err
		}
		params := make([]any, 0, len(r.o.args)-2)
		for _, a := range r.o.args[2:] {
			params = append(params, jsonOrString(a))
		}
		var res json.RawMessage
		if err := r.c.Conn().Call(context.Background(), sub, params, &res); err != nil {
			return nil, err
		}
		return res, nil
	}
	return nil, usageError(fmt.Sprintf("未知命令 %s", strings.TrimSpace(cmd+" "+sub)))
}

// jsonOrString parses s as JSON, or keeps it as a string.
func jsonOrString(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}

// ---- status ----

// StatusView is the `status` output.
type StatusView struct {
	Core         model.CoreState `json:"core"`
	CoreError    string          `json:"coreError,omitempty"`
	CoreVersion  string          `json:"coreVersion,omitempty"`
	Mode         model.Mode      `json:"mode"`
	SystemProxy  bool            `json:"systemProxy"`
	TUN          bool            `json:"tun"`
	TUNAvailable bool            `json:"tunAvailable"`
	MixedPort    int             `json:"mixedPort"`
	AllowLAN     bool            `json:"allowLAN"`
	Connections  int             `json:"connections"`
	Up           int64           `json:"up"`
	Down         int64           `json:"down"`
	Subscription string          `json:"subscription,omitempty"`
	MainGroup    string          `json:"mainGroup,omitempty"`
	Selected     string          `json:"selected,omitempty"`
}

func statusView(st model.Status, subs []model.Subscription, px model.ProxySnapshot) StatusView {
	v := StatusView{Core: st.Core, CoreError: st.CoreError, CoreVersion: st.CoreVersion, Mode: st.Mode,
		SystemProxy: st.SystemProxy, TUN: st.TUN, TUNAvailable: st.TUNAvailable, MixedPort: st.MixedPort,
		AllowLAN: st.AllowLAN, Connections: st.Connections, Up: st.Up, Down: st.Down, MainGroup: px.MainGroup}
	for _, s := range subs {
		if s.ID == st.CurrentSubscription {
			v.Subscription = s.Name
		}
	}
	for _, g := range px.Groups {
		if g.Name == px.MainGroup {
			v.Selected = g.Now
		}
	}
	return v
}

// ---- proxies ----

// GroupView is one group in `proxies list`.
type GroupView struct {
	Name    string       `json:"name"`
	Type    string       `json:"type"`
	Now     string       `json:"now,omitempty"`
	Members []MemberView `json:"members"`
}

// MemberView is a group member with its delay (ms; 0 untested, -1 failed).
type MemberView struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Delay int    `json:"delay"`
}

func (r *runner) proxiesList(only string) (any, error) {
	px := r.c.Proxies()
	if err := r.read(); err != nil {
		return nil, err
	}
	out := GroupsOf(px, only)
	if only != "" && len(out) == 0 {
		return nil, fmt.Errorf("找不到代理组「%s」", only)
	}
	return out, nil
}

// GroupsOf lists the visible groups (or only the one named).
func GroupsOf(px model.ProxySnapshot, only string) []GroupView {
	var out []GroupView
	for _, g := range px.Groups {
		if only != "" && g.Name != only {
			continue
		}
		if only == "" && (g.Hidden || g.Name == "GLOBAL") {
			continue
		}
		gv := GroupView{Name: g.Name, Type: string(g.Type), Now: g.Now}
		for _, m := range g.All {
			p := px.Proxies[m]
			gv.Members = append(gv.Members, MemberView{Name: m, Type: p.Type, Delay: p.Delay})
		}
		out = append(out, gv)
	}
	return out
}

// ---- subscriptions ----

func (r *runner) findSub(key string) (model.Subscription, error) {
	subs := r.c.Subscriptions()
	if err := r.read(); err != nil {
		return model.Subscription{}, err
	}
	for _, s := range subs {
		if s.ID == key {
			return s, nil
		}
	}
	for _, s := range subs {
		if s.Name == key {
			return s, nil
		}
	}
	return model.Subscription{}, fmt.Errorf("找不到订阅「%s」", key)
}

func (r *runner) subCmd(sub string) (any, error) {
	switch sub {
	case "list", "":
		subs := r.c.Subscriptions()
		return subs, r.read()
	case "add":
		if err := r.need(3, "sub add <链接>"); err != nil {
			return nil, err
		}
		s, err := r.c.ImportSubscriptionURL(r.arg(2), r.o.name, "", false, r.o.use)
		if err != nil {
			return nil, err
		}
		s.URL = ""
		return s, nil
	case "use":
		if err := r.need(3, "sub use <ID或名称>"); err != nil {
			return nil, err
		}
		s, err := r.findSub(r.arg(2))
		if err != nil {
			return nil, err
		}
		if err := r.c.UseSubscription(s.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "subscription": s.Name}, nil
	case "update":
		if r.o.all {
			if err := r.c.UpdateAllSubscriptions(); err != nil {
				return nil, err
			}
			return r.c.Subscriptions(), r.read()
		}
		key := r.arg(2)
		if key == "" {
			key = r.c.Status().CurrentSubscription
			if key == "" {
				return nil, errors.New("没有正在使用的订阅；请指定 ID 或名称，或用 --all")
			}
		}
		s, err := r.findSub(key)
		if err != nil {
			return nil, err
		}
		if err := r.c.UpdateSubscription(s.ID); err != nil {
			return nil, err
		}
		s, err = r.findSub(s.ID)
		return s, err
	}
	return nil, usageError("未知命令 sub " + sub)
}

// ---- rules ----

// RuleTypes are the custom rule types (model.CustomRule.Type).
var RuleTypes = []string{"DOMAIN-SUFFIX", "DOMAIN", "DOMAIN-KEYWORD", "IP-CIDR", "PROCESS-NAME"}

// RulesDiff is the custom rule list before and after adding r (placed
// first), as a line diff.
func RulesDiff(cur []model.CustomRule, r model.CustomRule) []model.DiffLine {
	out := []model.DiffLine{{Op: '+', Text: ruleLine(r)}}
	for _, c := range cur {
		out = append(out, model.DiffLine{Op: ' ', Text: ruleLine(c)})
	}
	return out
}

func ruleLine(r model.CustomRule) string { return r.Type + "," + r.Payload + "," + r.Target }

func (r *runner) ruleCmd(sub string) (any, error) {
	switch sub {
	case "add":
		if err := r.need(5, "rule add <类型> <内容> <目标>"); err != nil {
			return nil, err
		}
		typ := strings.ToUpper(r.arg(2))
		ok := false
		for _, t := range RuleTypes {
			ok = ok || t == typ
		}
		if !ok {
			return nil, usageError("规则类型只能是 " + strings.Join(RuleTypes, "、"))
		}
		rule := model.CustomRule{Type: typ, Payload: r.arg(3), Target: r.arg(4)}
		if r.o.dryRun {
			cur := r.c.CustomRules()
			if err := r.read(); err != nil {
				return nil, err
			}
			return map[string]any{"applied": false, "diff": DiffText(RulesDiff(cur, rule))}, nil
		}
		saved, err := r.c.SaveCustomRule(rule)
		if err != nil {
			return nil, err
		}
		return map[string]any{"applied": true, "rule": saved}, nil
	case "test":
		if err := r.need(3, "rule test <域名或IP>"); err != nil {
			return nil, err
		}
		return r.c.TestRule(r.arg(2))
	case "list", "":
		rules := r.c.CustomRules()
		return rules, r.read()
	}
	return nil, usageError("未知命令 rule " + sub)
}

// DiffText renders diff lines as "+ text" / "- text" / "  text".
func DiffText(lines []model.DiffLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		op := l.Op
		if op == 0 {
			op = ' '
		}
		out[i] = string(op) + " " + l.Text
	}
	return out
}

// ---- config ----

func (r *runner) configCmd(sub string) (any, error) {
	switch sub {
	case "get", "":
		var s map[string]any
		if err := r.c.Conn().CallAPI(context.Background(), "Settings", nil, &s); err != nil {
			return nil, err
		}
		if p := r.arg(2); p != "" {
			v, ok := lookupPath(s, p)
			if !ok {
				return nil, fmt.Errorf("没有设置项 %s", p)
			}
			return v, nil
		}
		return s, nil
	case "set":
		if err := r.need(4, "config set <字段路径> <值>"); err != nil {
			return nil, err
		}
		return r.patch(PathPatch(r.arg(2), jsonOrString(r.arg(3))))
	case "apply":
		if err := r.need(3, "config apply <文件|->"); err != nil {
			return nil, err
		}
		var b []byte
		var err error
		if r.arg(2) == "-" {
			b, err = io.ReadAll(r.env.Stdin)
		} else {
			b, err = os.ReadFile(r.arg(2))
		}
		if err != nil {
			return nil, err
		}
		var p map[string]any
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("补丁文件应为 JSON 对象: %v", err)
		}
		return r.patch(p)
	}
	return nil, usageError("未知命令 config " + sub)
}

// PatchView is the output of config set/apply.
type PatchView struct {
	Applied bool     `json:"applied"`
	Changed []string `json:"changed"`
	Diff    []string `json:"diff"`
}

func (r *runner) patch(p map[string]any) (any, error) {
	var res rpc.PatchResult
	if err := r.c.Conn().CallAPI(context.Background(), "PatchSettings", []any{p, r.o.dryRun}, &res); err != nil {
		return nil, err
	}
	return PatchView{Applied: res.Applied, Changed: res.Changed, Diff: DiffText(res.Diff)}, nil
}

// PathPatch turns "DNS.TakeOver" and a value into {"DNS":{"TakeOver":v}}.
func PathPatch(path string, v any) map[string]any {
	parts := strings.Split(path, ".")
	out := map[string]any{}
	cur := out
	for i, p := range parts {
		if i == len(parts)-1 {
			cur[p] = v
			break
		}
		next := map[string]any{}
		cur[p] = next
		cur = next
	}
	return out
}

func lookupPath(v any, path string) (any, bool) {
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		found := false
		for k, x := range m {
			if strings.EqualFold(k, p) {
				v, found = x, true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return v, true
}

// ---- output ----

func (r *runner) print(v any) error {
	w := r.env.Stdout
	if r.o.format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	defer tw.Flush()
	switch x := v.(type) {
	case []GroupView:
		for _, g := range x {
			fmt.Fprintf(tw, "%s\t%s\t→ %s\n", g.Name, g.Type, g.Now)
			for _, m := range g.Members {
				mark := " "
				if m.Name == g.Now {
					mark = "*"
				}
				fmt.Fprintf(tw, "  %s %s\t%s\t%s\n", mark, m.Name, m.Type, delayText(m.Delay))
			}
		}
		return nil
	case []model.Subscription:
		fmt.Fprintln(tw, "ID\t名称\t当前\t更新于\t错误")
		for _, s := range x {
			cur := ""
			if s.Current {
				cur = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, cur, s.UpdatedAt.Local().Format("2006-01-02 15:04"), s.LastError)
		}
		return nil
	case []rpc.AuditEntry:
		for _, e := range x {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Time.Local().Format("01-02 15:04:05"), e.Client, e.Params, e.Result)
		}
		return nil
	case []rpc.Method:
		for _, m := range x {
			fmt.Fprintf(tw, "%s\t%s\n", m.Name, m.Level)
		}
		return nil
	case PatchView:
		fmt.Fprintf(tw, "applied: %v\nchanged: %s\n", x.Applied, strings.Join(x.Changed, ", "))
		for _, l := range x.Diff {
			fmt.Fprintln(tw, l)
		}
		return nil
	}
	// Anything else: its JSON object as key/value lines.
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var j any
	_ = json.Unmarshal(b, &j)
	writeKV(tw, "", j)
	return nil
}

func delayText(d int) string {
	switch {
	case d < 0:
		return "超时"
	case d == 0:
		return "-"
	}
	return strconv.Itoa(d) + " ms"
}

func writeKV(w io.Writer, prefix string, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			writeKV(w, joinKey(prefix, k), x[k])
		}
	case []any:
		if len(x) == 0 {
			fmt.Fprintf(w, "%s\t[]\n", prefix)
		}
		for i, e := range x {
			writeKV(w, joinKey(prefix, strconv.Itoa(i)), e)
		}
	default:
		b, _ := json.Marshal(x)
		s := string(b)
		if str, ok := x.(string); ok {
			s = str
		}
		if prefix == "" {
			fmt.Fprintln(w, s)
			return
		}
		fmt.Fprintf(w, "%s\t%s\n", prefix, s)
	}
}

func joinKey(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}
