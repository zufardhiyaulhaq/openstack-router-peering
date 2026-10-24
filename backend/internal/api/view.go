package api

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/model"
)

// View is a peering as one side sees it (spec §4.2).
type View struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	StatusReason    string    `json:"status_reason,omitempty"`
	Role            string    `json:"role"`
	RouterID        string    `json:"router_id,omitempty"`
	PeerProjectID   string    `json:"peer_project_id"`
	PeerProjectName string    `json:"peer_project_name,omitempty"`
	NextHop         string    `json:"next_hop,omitempty"`
	PeerCIDRs       []string  `json:"peer_cidrs"`
	RouteHints      []string  `json:"route_hints"`
	Warnings        []string  `json:"warnings"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// BuildView renders p for callerProject. peerName is shown only to the accepter (it names the requester).
func BuildView(p model.Peering, callerProject, peerName string, peerCIDRs []netip.Prefix) View {
	v := View{ID: p.ID, Name: p.Name, Status: string(p.Status), StatusReason: p.StatusReason,
		PeerCIDRs: []string{}, RouteHints: []string{}, Warnings: append([]string{}, p.Warnings...),
		CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt}
	var hop netip.Addr
	if callerProject == p.AccepterProjectID {
		v.Role, v.RouterID, v.PeerProjectID, v.PeerProjectName, hop = "accepter", p.AccepterRouterID, p.RequesterProjectID, peerName, p.RequesterIP
	} else {
		v.Role, v.RouterID, v.PeerProjectID, hop = "requester", p.RequesterRouterID, p.AccepterProjectID, p.AccepterIP
	}
	for _, c := range peerCIDRs {
		v.PeerCIDRs = append(v.PeerCIDRs, c.String())
	}
	if hop.IsValid() {
		v.NextHop = hop.String()
		for _, c := range v.PeerCIDRs {
			v.RouteHints = append(v.RouteHints, fmt.Sprintf("openstack router set --route destination=%s,gateway=%s %s", c, v.NextHop, v.RouterID))
		}
	}
	return v
}
