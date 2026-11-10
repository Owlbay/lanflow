package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/locate"
	"lanflow/internal/model"
)

// 检测 page (docs/design.md §5.7 延伸): what an exit looks like from the
// outside, modelled on sites such as ippure.com, but measured by Lanflow
// itself and only when the user presses the button. Three probes, each
// kept in state.probe and published through TopicProbe as it progresses:
//
//   - IP 纯净度 (ProbeIP): the exit's address, place, ASN and kind from
//     the Cloudflare trace and the bundled databases (never a third
//     party); with online = true also the two free, keyless reputation
//     sources ip-api.com (proxy / hosting / mobile flags) and ippure.com
//     (fraud score, residential, broadcast), each labelled by name.
//   - 解锁检测 (ProbeUnlock): the service table of unlock.go through the
//     exit.
//   - 地区探查 (ProbeRegion): every node whose name places it in one
//     country: delay (the usual test), measured exit, optionally the
//     unlock checks.
//
// A node is reached the way exits.go probes do: selected in the hidden
// LANFLOW-PROBE group and requested through the core's probe listener;
// the direct connection uses a plain client. One exit probe runs at a
// time (probeExitMu) so two of them never fight over the probe group.

const (
	probeIPTimeout     = 40 * time.Second
	probeUnlockTimeout = 30 * time.Second
	probeOnlineTimeout = 10 * time.Second
	probeRegionTimeout = 10 * time.Minute

	ipAPIURL  = "http://ip-api.com/json/?fields=status,message,query,countryCode,isp,org,as,asname,mobile,proxy,hosting"
	ippureURL = "https://my.ippure.com/v1/info"

	srcTrace  = "Cloudflare trace"
	srcGeoDB  = "GeoLite2-ASN / GeoIP"
	srcIPAPI  = "ip-api.com"
	srcIppure = "ippure.com"
)

var (
	errProbeBusy       = i18n.Error("这个出口正在检测中，请稍候。")
	errRegionBusy      = i18n.Error("地区探查正在进行中，请稍候。")
	errProbePort       = i18n.Error("探测端口不可用")
	errProbeNoNode     = i18n.Error("请选择要检测的出口。")
	errRegionNoNodes   = i18n.Error("这个地区没有节点。")
	errProbeDirectMode = i18n.Error("直连模式下流量不经过节点，没有出口可以检测。")
)

// probeState is the 检测 page's part of Service.state (guarded by mu).
type probeState struct {
	ip          map[string]model.IPReport
	unlock      map[string]model.UnlockReport
	region      model.RegionReport
	regionCache map[string]string // node → ISO country guessed from the name
}

// Probe returns the state of every probe (copied).
func (s *Service) Probe() model.ProbeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.state.probe
	out := model.ProbeState{IP: make(map[string]model.IPReport, len(p.ip)), Unlock: make(map[string]model.UnlockReport, len(p.unlock)), Region: p.region}
	for k, v := range p.ip {
		v.Risk = append([]model.RiskInfo(nil), v.Risk...)
		v.Sources = append([]string(nil), v.Sources...)
		out.IP[k] = v
	}
	for k, v := range p.unlock {
		v.Results = append([]model.UnlockResult(nil), v.Results...)
		out.Unlock[k] = v
	}
	out.Region.Nodes = make([]model.RegionNode, len(p.region.Nodes))
	for i, n := range p.region.Nodes {
		n.Unlock = append([]model.UnlockResult(nil), n.Unlock...)
		out.Region.Nodes[i] = n
	}
	return out
}

// ProbeRegions groups the current nodes by the country their names
// suggest (the locator's name guess), sorted by node count.
func (s *Service) ProbeRegions() []model.ProbeRegion {
	s.mu.Lock()
	snap := s.state.proxies
	if s.state.probe.regionCache == nil {
		s.state.probe.regionCache = map[string]string{}
	}
	cache := s.state.probe.regionCache
	var names []string
	for _, p := range snap.Proxies {
		if _, isGroup := groupTypes[p.Type]; isGroup || p.Name == "DIRECT" || p.Name == "REJECT" || p.Name == probeGroup || p.Name == "GLOBAL" {
			continue
		}
		names = append(names, p.Name)
	}
	var missing []string
	for _, n := range names {
		if _, ok := cache[n]; !ok {
			missing = append(missing, n)
		}
	}
	s.mu.Unlock()
	if len(missing) > 0 {
		found := make(map[string]string, len(missing))
		for _, n := range missing {
			cc := ""
			if info := s.d.loc.Guess(n); info.Place != nil {
				cc = info.Place.Country
			}
			found[n] = cc
		}
		s.mu.Lock()
		for n, cc := range found {
			cache[n] = cc
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	byCountry := map[string][]string{}
	for _, n := range names {
		if cc := cache[n]; cc != "" {
			byCountry[cc] = append(byCountry[cc], n)
		}
	}
	s.mu.Unlock()
	out := make([]model.ProbeRegion, 0, len(byCountry))
	for cc, nodes := range byCountry {
		sort.Strings(nodes)
		out = append(out, model.ProbeRegion{Country: cc, Name: geo.CountryName(cc), Nodes: nodes})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Nodes) != len(out[j].Nodes) {
			return len(out[i].Nodes) > len(out[j].Nodes)
		}
		return out[i].Country < out[j].Country
	})
	return out
}

// exitClient prepares the HTTP path to target: the node selected in the
// probe group and a client through the probe listener, or a direct
// client. The caller holds probeExitMu.
func (s *Service) exitClient(ctx context.Context, target string, timeout time.Duration) (*http.Client, error) {
	if target == model.ProbeDirect {
		return s.d.httpDirect(timeout), nil
	}
	s.mu.Lock()
	port := s.state.probePort
	s.mu.Unlock()
	if port == 0 {
		return nil, errProbePort
	}
	if err := s.d.ctrl.SelectProxy(ctx, probeGroup, target); err != nil {
		return nil, fmt.Errorf(i18n.T("切换探测节点「%s」失败：%v"), target, err)
	}
	return s.d.httpVia(port, timeout), nil
}

// measureExit finds the exit endpoint of target: the user's own public
// endpoint for the direct connection, else the locator's probe through
// the node (serialized by the locator, cached 24 h for exits.go; here it
// is always measured anew by the probe itself).
func (s *Service) measureExit(ctx context.Context, target string) (model.ExitInfo, error) {
	if target == model.ProbeDirect {
		ep, err := s.d.loc.Direct(ctx)
		if err != nil {
			return model.ExitInfo{}, err
		}
		info := model.ExitInfo{Node: target, Source: model.LocateProbe, Exit: ep, MeasuredAt: s.d.now()}
		if ep.Place != nil {
			p := *ep.Place
			info.Place = &p
		}
		return info, nil
	}
	s.mu.Lock()
	port := s.state.probePort
	s.mu.Unlock()
	if port == 0 {
		return model.ExitInfo{}, errProbePort
	}
	sel := func(ctx context.Context, n string) error { return s.d.ctrl.SelectProxy(ctx, probeGroup, n) }
	info, err := s.d.loc.Probe(ctx, sel, port, target)
	if err != nil {
		return model.ExitInfo{}, err
	}
	info.Node = target
	s.storeExit(info) // the exit map benefits too
	return info, nil
}

// checkTarget validates a probe target against the running core.
func (s *Service) checkTarget(target string) error {
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	snap := s.state.proxies
	s.mu.Unlock()
	if target == "" {
		return errProbeNoNode
	}
	if target == model.ProbeDirect {
		return nil
	}
	if !running {
		return errCoreNotRunning
	}
	if p, ok := snap.Proxies[target]; !ok {
		return fmt.Errorf(i18n.T("找不到节点「%s」"), target)
	} else if _, isGroup := groupTypes[p.Type]; isGroup {
		return fmt.Errorf(i18n.T("「%s」是代理组，请选择具体节点"), target)
	}
	return nil
}

// ---- IP 纯净度 ----

// ProbeIP measures target's exit in the background: the trace and the
// local databases always; with online also ip-api.com and ippure.com
// through the same exit. Progress and the result land in Probe().IP
// (TopicProbe).
func (s *Service) ProbeIP(target string, online bool) error {
	if err := s.checkTarget(target); err != nil {
		return err
	}
	s.mu.Lock()
	if s.state.probe.ip == nil {
		s.state.probe.ip = map[string]model.IPReport{}
	}
	if cur, ok := s.state.probe.ip[target]; ok && cur.Running {
		s.mu.Unlock()
		return errProbeBusy
	}
	s.state.probe.ip[target] = model.IPReport{Target: target, Running: true}
	s.mu.Unlock()
	s.em.emit(TopicProbe)
	s.goBackground(func() { s.probeIP(target, online) })
	return nil
}

func (s *Service) probeIP(target string, online bool) {
	ctx, cancel := context.WithTimeout(s.rootCtx, probeIPTimeout)
	defer cancel()
	s.probeExitMu.Lock()
	defer s.probeExitMu.Unlock()

	rep := model.IPReport{Target: target}
	info, err := s.measureExit(ctx, target)
	if err != nil {
		rep.Error = err.Error()
	} else {
		rep = ipReportFrom(target, info)
		if online {
			client, cerr := s.exitClient(ctx, target, probeOnlineTimeout)
			if cerr != nil {
				rep.RiskError = cerr.Error()
			} else {
				rep.Risk, rep.RiskError = fetchRisk(ctx, client)
				for _, r := range rep.Risk {
					rep.Sources = append(rep.Sources, r.Source)
				}
				applyRisk(&rep)
			}
		}
	}
	rep.CheckedAt = s.d.now()
	s.mu.Lock()
	s.state.probe.ip[target] = rep
	s.mu.Unlock()
	s.em.emit(TopicProbe)
}

// ipReportFrom builds the offline part of a report from a measured exit.
func ipReportFrom(target string, info model.ExitInfo) model.IPReport {
	rep := model.IPReport{Target: target, IP: info.Exit.IP, ASN: info.Exit.ASN, Org: info.Exit.ISP, Colo: info.Colo, Sources: []string{srcTrace}}
	if info.Place != nil {
		p := *info.Place
		rep.Place = &p
	}
	if info.Exit.Place != nil {
		rep.DBCountry = info.Exit.Place.Country
	}
	if rep.ASN != "" || rep.Org != "" || rep.DBCountry != "" {
		rep.Sources = append(rep.Sources, srcGeoDB)
	}
	rep.Kind = locate.Kind(rep.ASN, rep.Org)
	// Native vs broadcast, offline: the database's country for the block
	// against where Cloudflare saw the request come from. Weak but free.
	geoCC := ""
	if rep.Place != nil {
		geoCC = rep.Place.Country
	}
	switch {
	case rep.DBCountry == "" || geoCC == "":
		rep.Native = model.NativeUnknown
	case strings.EqualFold(rep.DBCountry, geoCC):
		rep.Native = model.NativeYes
		rep.NativeWhy = i18n.Tf("数据库归属 %s，Cloudflare 判定 %s，一致（本地推断）", geo.CountryName(rep.DBCountry), geo.CountryName(geoCC))
	default:
		rep.Native = model.NativeBroadcast
		rep.NativeWhy = i18n.Tf("数据库归属 %s，Cloudflare 判定 %s，不一致（本地推断）", geo.CountryName(rep.DBCountry), geo.CountryName(geoCC))
	}
	return rep
}

// applyRisk lets the online sources refine the kind and the native verdict.
func applyRisk(rep *model.IPReport) {
	for _, r := range rep.Risk {
		if r.HasFlags {
			switch {
			case r.Mobile:
				rep.Kind = model.IPKindMobile
			case r.Hosting:
				rep.Kind = model.IPKindDatacenter
			case rep.Kind == model.IPKindUnknown:
				rep.Kind = model.IPKindResidential
			}
		}
		if r.HasScore && r.Residential {
			rep.Kind = model.IPKindResidential
		}
		if r.HasBroadcast {
			if r.Broadcast {
				rep.Native = model.NativeBroadcast
			} else {
				rep.Native = model.NativeYes
			}
			rep.NativeWhy = i18n.Tf("%s 判定", r.Source)
		}
	}
}

// fetchRisk asks both free sources through client; each that answers
// adds a RiskInfo, the failures are joined into err text.
func fetchRisk(ctx context.Context, client *http.Client) ([]model.RiskInfo, string) {
	var out []model.RiskInfo
	var errs []string
	if r, err := fetchIPAPI(ctx, client, ipAPIURL); err != nil {
		errs = append(errs, srcIPAPI+": "+err.Error())
	} else {
		out = append(out, r)
	}
	if r, err := fetchIppure(ctx, client, ippureURL); err != nil {
		errs = append(errs, srcIppure+": "+err.Error())
	} else {
		out = append(out, r)
	}
	return out, strings.Join(errs, "; ")
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Lanflow/0.1 ip-check")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// fetchIPAPI reads ip-api.com's free JSON endpoint (no key, HTTP only,
// 45 requests a minute): the proxy / hosting / mobile flags.
func fetchIPAPI(ctx context.Context, client *http.Client, url string) (model.RiskInfo, error) {
	var v struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		CountryCode string `json:"countryCode"`
		Mobile      bool   `json:"mobile"`
		Proxy       bool   `json:"proxy"`
		Hosting     bool   `json:"hosting"`
	}
	if err := getJSON(ctx, client, url, &v); err != nil {
		return model.RiskInfo{}, err
	}
	if v.Status != "success" {
		if v.Message == "" {
			v.Message = "status " + v.Status
		}
		return model.RiskInfo{}, errors.New(v.Message)
	}
	return model.RiskInfo{Source: srcIPAPI, HasFlags: true, Proxy: v.Proxy, Hosting: v.Hosting, Mobile: v.Mobile, RegionOfBlock: strings.ToUpper(v.CountryCode)}, nil
}

// fetchIppure reads ippure.com's public info endpoint (no key): fraud
// score, residential and broadcast verdicts.
func fetchIppure(ctx context.Context, client *http.Client, url string) (model.RiskInfo, error) {
	var v struct {
		IP            string          `json:"ip"`
		CountryCode   string          `json:"countryCode"`
		FraudScore    json.Number     `json:"fraudScore"`
		IsResidential bool            `json:"isResidential"`
		IsBroadcast   json.RawMessage `json:"isBroadcast"`
	}
	if err := getJSON(ctx, client, url, &v); err != nil {
		return model.RiskInfo{}, err
	}
	if v.IP == "" {
		return model.RiskInfo{}, errors.New(i18n.T("响应中没有 ip 字段"))
	}
	r := model.RiskInfo{Source: srcIppure, HasScore: true, Residential: v.IsResidential, RegionOfBlock: strings.ToUpper(v.CountryCode)}
	if f, err := v.FraudScore.Float64(); err == nil {
		r.Score = int(f)
	}
	if len(v.IsBroadcast) > 0 && string(v.IsBroadcast) != "null" {
		r.HasBroadcast = true
		r.Broadcast = string(v.IsBroadcast) == "true"
	}
	return r, nil
}

// ---- 解锁检测 ----

// ProbeUnlock runs the unlock table through target in the background;
// results arrive one by one in Probe().Unlock[target] (TopicProbe).
func (s *Service) ProbeUnlock(target string) error {
	if err := s.checkTarget(target); err != nil {
		return err
	}
	s.mu.Lock()
	if s.state.probe.unlock == nil {
		s.state.probe.unlock = map[string]model.UnlockReport{}
	}
	if cur, ok := s.state.probe.unlock[target]; ok && cur.Running {
		s.mu.Unlock()
		return errProbeBusy
	}
	rep := model.UnlockReport{Target: target, Running: true, Results: make([]model.UnlockResult, len(unlockChecks))}
	for i, ck := range unlockChecks {
		rep.Results[i] = model.UnlockResult{Service: ck.Name, Status: model.UnlockPending}
	}
	s.state.probe.unlock[target] = rep
	s.mu.Unlock()
	s.em.emit(TopicProbe)
	s.goBackground(func() { s.probeUnlock(target) })
	return nil
}

func (s *Service) probeUnlock(target string) {
	ctx, cancel := context.WithTimeout(s.rootCtx, probeUnlockTimeout)
	defer cancel()
	s.probeExitMu.Lock()
	defer s.probeExitMu.Unlock()

	client, err := s.exitClient(ctx, target, unlockTimeout)
	var results []model.UnlockResult
	if err == nil {
		results = runUnlockChecks(ctx, client, unlockChecks, func(i int, r model.UnlockResult) {
			s.mu.Lock()
			rep := s.state.probe.unlock[target]
			if i < len(rep.Results) {
				rep.Results[i] = r
				rep.Done++
			}
			s.state.probe.unlock[target] = rep
			s.mu.Unlock()
			s.em.emit(TopicProbe)
		})
	}
	s.mu.Lock()
	rep := s.state.probe.unlock[target]
	rep.Running = false
	rep.CheckedAt = s.d.now()
	if err != nil {
		rep.Error = err.Error()
	} else {
		rep.Results = results
		rep.Done = len(results)
	}
	s.state.probe.unlock[target] = rep
	s.mu.Unlock()
	s.em.emit(TopicProbe)
}

// ---- 地区探查 ----

// ProbeRegion tests every node of country (ProbeRegions) in the
// background: delay first (all at once, the usual test), then exit and,
// with withUnlock, the unlock table, node by node. Probe().Region follows
// along (TopicProbe); the delay column can also be read live from
// Proxies().
func (s *Service) ProbeRegion(country string, withUnlock bool) error {
	if !s.running() {
		return errCoreNotRunning
	}
	s.mu.Lock()
	mode := s.state.st.Mode
	busy := s.state.probe.region.Running
	s.mu.Unlock()
	if mode == model.ModeDirect {
		return errProbeDirectMode
	}
	if busy {
		return errRegionBusy
	}
	var nodes []string
	for _, r := range s.ProbeRegions() {
		if r.Country == country {
			nodes = r.Nodes
		}
	}
	if len(nodes) == 0 {
		return errRegionNoNodes
	}
	rep := model.RegionReport{Country: country, Running: true, WithUnlock: withUnlock, Nodes: make([]model.RegionNode, len(nodes))}
	for i, n := range nodes {
		rep.Nodes[i] = model.RegionNode{Node: n, Testing: true}
	}
	s.mu.Lock()
	if s.state.probe.region.Running {
		s.mu.Unlock()
		return errRegionBusy
	}
	s.state.probe.region = rep
	s.mu.Unlock()
	s.em.emit(TopicProbe)
	s.goBackground(func() { s.probeRegion(country, nodes, withUnlock) })
	return nil
}

func (s *Service) probeRegion(country string, nodes []string, withUnlock bool) {
	ctx, cancel := context.WithTimeout(s.rootCtx, probeRegionTimeout)
	defer cancel()

	update := func(i int, fn func(n *model.RegionNode)) {
		s.mu.Lock()
		rep := &s.state.probe.region
		if rep.Country == country && i < len(rep.Nodes) {
			fn(&rep.Nodes[i])
		}
		s.mu.Unlock()
		s.em.emit(TopicProbe)
	}

	// Delays, all nodes at once (testNodes limits the concurrency itself).
	s.testNodes(nodes)
	s.mu.Lock()
	snap := s.state.proxies
	s.mu.Unlock()
	for i, n := range nodes {
		d := snap.Proxies[n].Delay
		update(i, func(rn *model.RegionNode) { rn.Delay, rn.Testing = d, false })
	}

	// Exits (and unlock), one node at a time: the probe group holds one
	// selection.
	s.probeExitMu.Lock()
	for i, n := range nodes {
		if ctx.Err() != nil {
			break
		}
		nctx, ncancel := context.WithTimeout(ctx, probeTimeout+probeUnlockTimeout)
		info, err := s.measureExit(nctx, n)
		var unlock []model.UnlockResult
		if err == nil && withUnlock {
			if client, cerr := s.exitClient(nctx, n, unlockTimeout); cerr == nil {
				unlock = runUnlockChecks(nctx, client, unlockChecks, nil)
			} else {
				err = cerr
			}
		}
		ncancel()
		update(i, func(rn *model.RegionNode) {
			rn.Probed = err == nil
			if err != nil {
				rn.Error = err.Error()
			} else {
				rn.Exit = info.Exit
				if info.Place != nil {
					p := *info.Place
					rn.Exit.Place = &p
				}
			}
			rn.Unlock = unlock
		})
		s.mu.Lock()
		if s.state.probe.region.Country == country {
			s.state.probe.region.Done = i + 1
		}
		s.mu.Unlock()
	}
	s.probeExitMu.Unlock()

	s.mu.Lock()
	if s.state.probe.region.Country == country {
		s.state.probe.region.Running = false
		s.state.probe.region.CheckedAt = s.d.now()
		if ctx.Err() != nil {
			s.state.probe.region.Error = i18n.T("探查超时，未完成的节点已跳过")
		}
	}
	s.mu.Unlock()
	s.em.emit(TopicProbe)
}
