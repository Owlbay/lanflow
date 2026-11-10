package app

import "lanflow/internal/model"

// LookupIP resolves an IP offline through the locator's GeoIP/ASN
// databases (docs/exit-map.md §4, layer B). It never touches the network.
func (s *Service) LookupIP(ip string) model.EndpointInfo {
	if s.d.loc == nil {
		return model.EndpointInfo{IP: ip}
	}
	return s.d.loc.Lookup(ip)
}
