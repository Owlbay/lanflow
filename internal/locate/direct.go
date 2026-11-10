package locate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// direct fetches the trace over the physical interface, the socket bound
// to it so a TUN (ours or another client's) cannot capture the request:
// the user's own public endpoint. Without a physical interface, or when
// binding fails, it reports an error and never retries unbound, which
// would measure the proxy's exit instead.
func (l *locator) direct(ctx context.Context) (model.EndpointInfo, error) {
	client, iface, err := l.directClient(ctx, probeTimeout)
	if err != nil {
		return model.EndpointInfo{}, fmt.Errorf(i18n.T("找不到物理网卡，无法实测本机出口：%v"), err)
	}
	tr, err := l.fetchTrace(ctx, client)
	if err != nil {
		if errors.Is(err, netx.ErrBind) {
			return model.EndpointInfo{}, fmt.Errorf(i18n.T("无法绑定物理网卡 %s，未实测本机出口：%v"), iface, err)
		}
		return model.EndpointInfo{}, fmt.Errorf(i18n.T("经物理网卡 %s 获取本机出口信息失败：%v"), iface, err)
	}
	ep := l.resolve(tr.IP)
	ep.BoundIface = iface
	// For the user's own location the colo is only trusted when it is in the
	// same country as the IP: traffic from places without a nearby PoP is
	// served from abroad (e.g. mainland China → LAX/HKG).
	if p, ok := geo.FromTrace(tr.Loc, tr.Colo); ok {
		if strings.EqualFold(p.Country, tr.Loc) || tr.Loc == "" {
			ep.Place = &p
		} else if ep.Place == nil {
			if c, ok := geo.Country(tr.Loc); ok {
				ep.Place = &c
			}
		}
	}
	return ep, nil
}
