// Package locate finds where nodes really exit and analyses the path.
//
// Exported signatures are a stable contract; bodies live in the other
// files of this package. See docs/exit-map.md (§1, §3, §4A).
package locate

import (
	"context"

	"lanflow/internal/model"
)

// TraceURL is the Cloudflare trace endpoint used for probes.
const TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// SelectFunc switches the hidden LANFLOW-PROBE group to node (via the controller).
type SelectFunc func(ctx context.Context, node string) error

// Locator caches probe results (24 h) in a JSON file and looks IPs up in
// the core's GeoIP (MMDB, country) and ASN databases.
type Locator struct{ l *locator }

// New returns a Locator. cachePath is a JSON file; mmdbPath and asnPath are
// the core's geoip.metadb (or Country.mmdb) and GeoLite2-ASN.mmdb; missing
// databases degrade gracefully (fields left empty).
func New(cachePath, mmdbPath, asnPath string) *Locator {
	return &Locator{newLocator(cachePath, mmdbPath, asnPath)}
}

// Guess answers without network: a probe cached within 24 h, else the
// node-name guess (Source LocateName), else LocateUnknown.
func (l *Locator) Guess(node string) model.ExitInfo { return l.l.guess(node) }

// Probe measures node: selects it in LANFLOW-PROBE via sel, fetches
// TraceURL through 127.0.0.1:probePort (netx.ViaPort), parses ip/loc/colo,
// looks the IP up, caches and returns. One probe at a time (serialized).
func (l *Locator) Probe(ctx context.Context, sel SelectFunc, probePort int, node string) (model.ExitInfo, error) {
	return l.l.probe(ctx, sel, probePort, node)
}

// Direct fetches TraceURL directly (netx.Direct) and returns the user's public endpoint.
func (l *Locator) Direct(ctx context.Context) (model.EndpointInfo, error) { return l.l.direct(ctx) }

// Lookup resolves an IP offline (country place + ISP + ASN).
func (l *Locator) Lookup(ip string) model.EndpointInfo { return l.l.lookup(ip) }

// Analyze fills info.Entry (from entryIP = the connection's
// remoteDestination, looked up offline), info.Path and info.PathNote:
// PathChain when chainHops > 1; PathSuspicious when the exit IP's country
// differs from the colo's country or the name's country differs from the
// measured one (note e.g. "名称为台湾，实测出口在香港"); PathRelay when
// entry and exit cities differ; else PathDirect.
func (l *Locator) Analyze(info *model.ExitInfo, entryIP string, chainHops int) {
	l.l.analyze(info, entryIP, chainHops)
}
