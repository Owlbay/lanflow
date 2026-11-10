package locate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"lanflow/internal/geo"
	"lanflow/internal/model"
)

// trace is the parsed body of a Cloudflare /cdn-cgi/trace response.
type trace struct {
	IP   string
	Loc  string // ISO country of the client IP, as seen by Cloudflare
	Colo string // IATA code of the responding data centre
}

// probe selects node in the probe group, requests the trace through the
// probe port and caches the result. Probes are serialized.
func (l *locator) probe(ctx context.Context, sel SelectFunc, probePort int, node string) (model.ExitInfo, error) {
	l.probeMu.Lock()
	defer l.probeMu.Unlock()

	if sel == nil {
		return model.ExitInfo{}, errors.New("探测失败：没有可用的节点切换函数")
	}
	if err := sel(ctx, node); err != nil {
		return model.ExitInfo{}, fmt.Errorf("切换探测节点「%s」失败：%w", node, err)
	}

	start := l.now()
	tr, err := l.fetchTrace(ctx, l.proxyClient(probePort, probeTimeout))
	if err != nil {
		return model.ExitInfo{}, fmt.Errorf("经节点「%s」探测出口失败：%w", node, err)
	}
	rtt := l.now().Sub(start)

	exit := l.resolve(tr.IP)
	info := model.ExitInfo{
		Node:       node,
		Source:     model.LocateProbe,
		Exit:       exit,
		Colo:       tr.Colo,
		MeasuredAt: start,
	}
	if p, ok := geo.FromTrace(tr.Loc, tr.Colo); ok {
		info.Place = &p
		if info.Exit.Place == nil {
			info.Exit.Place = &p
		}
	} else if exit.Place != nil {
		p := *exit.Place
		info.Place = &p
	}

	if err := l.store(cacheEntry{ExitInfo: info, ProbeRTT: rtt}); err != nil {
		// The measurement is still good; only persistence failed.
		return info, fmt.Errorf("保存出口缓存失败：%w", err)
	}
	return info, nil
}

// fetchTrace GETs the trace URL with client and parses the key=value body.
func (l *locator) fetchTrace(ctx context.Context, client *http.Client) (trace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.traceURL, nil)
	if err != nil {
		return trace{}, err
	}
	req.Header.Set("User-Agent", "Lanflow/0.1 exit-probe")
	resp, err := client.Do(req)
	if err != nil {
		return trace{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return trace{}, fmt.Errorf("Cloudflare 返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return trace{}, err
	}
	tr := parseTrace(string(body))
	if tr.IP == "" {
		return trace{}, errors.New("响应中没有 ip 字段，可能被中间设备劫持")
	}
	return tr, nil
}

// parseTrace reads "key=value" lines; unknown keys are ignored.
func parseTrace(body string) trace {
	var tr trace
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "ip":
			tr.IP = v
		case "loc":
			tr.Loc = strings.ToUpper(v)
		case "colo":
			tr.Colo = strings.ToUpper(v)
		}
	}
	return tr
}

// probeRTT returns the cached probe duration for node (0 when unknown).
func (l *locator) probeRTT(node string) time.Duration {
	if e, ok := l.cached(node); ok {
		return e.ProbeRTT
	}
	return 0
}
