package fake

import (
	"strings"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// The 检测 page's sample data: every probe finishes after the usual
// delay with plausible results, so the page can be worked on and tested
// without a core.

// fakeRegionOf is the country a sample node's name suggests.
func fakeRegionOf(node string) string {
	for prefix, cc := range map[string]string{"香港": "HK", "东京": "JP", "新加坡": "SG", "台湾": "TW", "美国": "US", "英国": "GB"} {
		if strings.HasPrefix(node, prefix) {
			return cc
		}
	}
	return ""
}

// Probe returns the sample state (copied).
func (f *Fake) Probe() model.ProbeState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := model.ProbeState{IP: map[string]model.IPReport{}, Unlock: map[string]model.UnlockReport{}, Region: f.probe.Region}
	for k, v := range f.probe.IP {
		v.Risk = append([]model.RiskInfo(nil), v.Risk...)
		v.Sources = append([]string(nil), v.Sources...)
		out.IP[k] = v
	}
	for k, v := range f.probe.Unlock {
		v.Results = append([]model.UnlockResult(nil), v.Results...)
		out.Unlock[k] = v
	}
	out.Region.Nodes = append([]model.RegionNode(nil), f.probe.Region.Nodes...)
	return out
}

// ProbeRegions groups the sample nodes by name.
func (f *Fake) ProbeRegions() []model.ProbeRegion {
	var out []model.ProbeRegion
	idx := map[string]int{}
	for _, n := range nodes {
		cc := fakeRegionOf(n)
		if cc == "" {
			continue
		}
		i, ok := idx[cc]
		if !ok {
			i = len(out)
			idx[cc] = i
			out = append(out, model.ProbeRegion{Country: cc, Name: geo.CountryName(cc)})
		}
		out[i].Nodes = append(out[i].Nodes, n)
	}
	return out
}

func (f *Fake) probeTarget(target string) error {
	if target == "" {
		return i18n.Error("请选择要检测的出口。")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if target != model.ProbeDirect && f.st.Core != model.CoreRunning {
		return i18n.Error("内核未启动。请先启动内核。")
	}
	if f.probe.IP == nil {
		f.probe.IP = map[string]model.IPReport{}
		f.probe.Unlock = map[string]model.UnlockReport{}
	}
	return nil
}

// ProbeIP fills a sample report: the direct connection is a residential
// 中国电信 address, nodes get the exit of exitOf; online adds both sources.
func (f *Fake) ProbeIP(target string, online bool) error {
	if err := f.probeTarget(target); err != nil {
		return err
	}
	f.mu.Lock()
	f.probe.IP[target] = model.IPReport{Target: target, Running: true}
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	f.wait()
	rep := model.IPReport{Target: target, Sources: []string{"Cloudflare trace", "GeoLite2-ASN / GeoIP"}, CheckedAt: time.Now()}
	if target == model.ProbeDirect {
		you := f.You()
		rep.IP, rep.Place, rep.ASN, rep.Org = you.IP, you.Place, you.ASN, you.ISP
		rep.Kind, rep.Native, rep.DBCountry = model.IPKindResidential, model.NativeYes, "CN"
		rep.NativeWhy = i18n.Tf("数据库归属 %s，Cloudflare 判定 %s，一致（本地推断）", "CN", "CN")
		if online {
			rep.Risk = []model.RiskInfo{
				{Source: "ip-api.com", HasFlags: true, RegionOfBlock: "CN"},
				{Source: "ippure.com", HasScore: true, Score: 8, Residential: true, HasBroadcast: true, RegionOfBlock: "CN"},
			}
		}
	} else {
		x := exitOf(target)
		rep.IP, rep.Place, rep.ASN, rep.Org, rep.Colo = x.Exit.IP, x.Place, x.Exit.ASN, x.Exit.ISP, x.Colo
		rep.Kind = model.IPKindDatacenter
		rep.DBCountry = "US"
		if x.Place != nil {
			rep.Native = model.NativeBroadcast
			rep.NativeWhy = i18n.Tf("数据库归属 %s，Cloudflare 判定 %s，不一致（本地推断）", "US", x.Place.Country)
		}
		if online {
			rep.Risk = []model.RiskInfo{
				{Source: "ip-api.com", HasFlags: true, Hosting: true, Proxy: strings.HasPrefix(target, "美国"), RegionOfBlock: "US"},
				{Source: "ippure.com", HasScore: true, Score: 42, HasBroadcast: true, Broadcast: true, RegionOfBlock: "US"},
			}
			rep.NativeWhy = i18n.Tf("%s 判定", "ippure.com")
		}
	}
	if online {
		for _, r := range rep.Risk {
			rep.Sources = append(rep.Sources, r.Source)
		}
	}
	f.mu.Lock()
	f.probe.IP[target] = rep
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	return nil
}

// fakeUnlock is the sample verdict table for an exit in country cc.
func fakeUnlock(cc string) []model.UnlockResult {
	yes := func(name, region string) model.UnlockResult {
		return model.UnlockResult{Service: name, Status: model.UnlockYes, Region: region, Millis: 180 + len(name)*17}
	}
	out := []model.UnlockResult{
		yes("ChatGPT", cc), yes("Claude", cc), yes("Gemini", ""),
		{Service: "Netflix", Status: model.UnlockPartial, Region: cc, Note: i18n.T("仅自制剧"), Millis: 640},
		yes("YouTube Premium", cc),
		yes("Disney+", cc), yes("TikTok", cc), yes("Spotify", cc),
		yes("Google 搜索", ""), {Service: "Bing", Status: model.UnlockYes, Region: cc, Note: i18n.T("国际版"), Millis: 120},
	}
	if cc == "CN" {
		for i := range out {
			out[i].Status, out[i].Note = model.UnlockNo, i18n.T("所在地区不支持")
			if out[i].Service == "Bing" {
				out[i].Status, out[i].Note = model.UnlockYes, i18n.T("跳转到国内版 cn.bing.com")
			}
		}
	}
	if cc == "HK" {
		out[0] = model.UnlockResult{Service: "ChatGPT", Status: model.UnlockNo, Region: cc, Note: i18n.T("所在地区不支持"), Millis: 300}
	}
	return out
}

// ProbeUnlock fills the sample verdicts for target.
func (f *Fake) ProbeUnlock(target string) error {
	if err := f.probeTarget(target); err != nil {
		return err
	}
	cc := "CN"
	if target != model.ProbeDirect {
		if x := exitOf(target); x.Place != nil {
			cc = x.Place.Country
		}
	}
	results := fakeUnlock(cc)
	pending := make([]model.UnlockResult, len(results))
	for i, r := range results {
		pending[i] = model.UnlockResult{Service: r.Service, Status: model.UnlockPending}
	}
	f.mu.Lock()
	f.probe.Unlock[target] = model.UnlockReport{Target: target, Running: true, Results: pending}
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	f.wait()
	f.mu.Lock()
	f.probe.Unlock[target] = model.UnlockReport{Target: target, Results: results, Done: len(results), CheckedAt: time.Now()}
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	return nil
}

// ProbeRegion fills sample rows for every node of country.
func (f *Fake) ProbeRegion(country string, withUnlock bool) error {
	f.mu.Lock()
	running := f.st.Core == model.CoreRunning
	direct := f.st.Mode == model.ModeDirect
	busy := f.probe.Region.Running
	f.mu.Unlock()
	if !running {
		return i18n.Error("内核未启动。请先启动内核。")
	}
	if direct {
		return i18n.Error("直连模式下流量不经过节点，没有出口可以检测。")
	}
	if busy {
		return i18n.Error("地区探查正在进行中，请稍候。")
	}
	var names []string
	for _, r := range f.ProbeRegions() {
		if r.Country == country {
			names = r.Nodes
		}
	}
	if len(names) == 0 {
		return i18n.Error("这个地区没有节点。")
	}
	rep := model.RegionReport{Country: country, Running: true, WithUnlock: withUnlock}
	for _, n := range names {
		rep.Nodes = append(rep.Nodes, model.RegionNode{Node: n, Testing: true})
	}
	f.mu.Lock()
	f.probe.Region = rep
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	f.wait()
	f.mu.Lock()
	for i, n := range names {
		x := exitOf(n)
		row := model.RegionNode{Node: n, Delay: f.px.Proxies[n].Delay, Probed: x.Place != nil, Exit: x.Exit}
		if x.Place != nil {
			p := *x.Place
			row.Exit.Place = &p
			if withUnlock {
				row.Unlock = fakeUnlock(p.Country)
			}
		} else {
			row.Error = i18n.T("超时")
		}
		if row.Delay == 0 {
			row.Delay = 95 + i*31
		}
		f.probe.Region.Nodes[i] = row
		f.probe.Region.Done = i + 1
	}
	f.probe.Region.Running = false
	f.probe.Region.CheckedAt = time.Now()
	f.mu.Unlock()
	f.emit(app.TopicProbe)
	return nil
}
