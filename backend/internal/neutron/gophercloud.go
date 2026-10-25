package neutron

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

// subnetBatch is how many subnet IDs go into one lookup (about 40 bytes of query string each).
const subnetBatch = 50

// Neutron implements Client over a gophercloud network ServiceClient.
type Neutron struct {
	sc               *gophercloud.ServiceClient
	serviceProjectID string
}

var _ Client = (*Neutron)(nil)

// NewNeutron checks that Neutron supports tags and returns a client acting as the service account.
func NewNeutron(ctx context.Context, sc *gophercloud.ServiceClient, serviceProjectID string) (*Neutron, error) {
	n := &Neutron{sc: sc, serviceProjectID: serviceProjectID}
	var resp struct {
		Extensions []struct {
			Alias string `json:"alias"`
		} `json:"extensions"`
	}
	if err := n.get(ctx, n.sc.ServiceURL("extensions"), &resp); err != nil {
		return nil, err
	}
	for _, e := range resp.Extensions {
		if e.Alias == "standard-attr-tag" {
			return n, nil
		}
	}
	return nil, errors.New("neutron extension standard-attr-tag is required")
}

// wrap turns a gophercloud error into *APIError when Neutron answered.
func wrap(err error) error {
	var uc gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &uc) {
		e := &APIError{Code: uc.Actual}
		var body struct {
			NeutronError struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"NeutronError"`
		}
		if json.Unmarshal(uc.Body, &body) == nil {
			e.Type, e.Message = body.NeutronError.Type, body.NeutronError.Message
		}
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(uc.Body))
		}
		return e
	}
	return err
}

func (n *Neutron) get(ctx context.Context, u string, out any) error {
	_, err := n.sc.Get(ctx, u, out, &gophercloud.RequestOpts{OkCodes: []int{200}})
	return wrap(err)
}

func (n *Neutron) post(ctx context.Context, u string, body, out any) error {
	_, err := n.sc.Post(ctx, u, body, out, &gophercloud.RequestOpts{OkCodes: []int{201}})
	return wrap(err)
}

func (n *Neutron) put(ctx context.Context, u string, body, out any) error {
	_, err := n.sc.Put(ctx, u, body, out, &gophercloud.RequestOpts{OkCodes: []int{200, 201, 204}})
	return wrap(err)
}

func (n *Neutron) del(ctx context.Context, u string) error {
	_, err := n.sc.Delete(ctx, u, &gophercloud.RequestOpts{OkCodes: []int{204}})
	return wrap(err)
}

func (n *Neutron) query(path string, q url.Values) string {
	return n.sc.ServiceURL(path) + "?" + q.Encode()
}

// tagAfterCreate sets the tag with its own PUT. Tags are never sent in a create body: some clouds advertise
// tag-creation and still answer 400 "Attribute 'tags' not allowed in POST" (seen on 2026.1 ML2/OVN).
func (n *Neutron) tagAfterCreate(ctx context.Context, kind, id, tag string) error {
	_, err := n.sc.Put(ctx, n.sc.ServiceURL(kind, id, "tags", tag), nil, nil, &gophercloud.RequestOpts{OkCodes: []int{201, 204}})
	return wrap(err)
}

type rawPort struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	NetworkID   string `json:"network_id"`
	DeviceID    string `json:"device_id"`
	DeviceOwner string `json:"device_owner"`
	FixedIPs    []struct {
		SubnetID  string `json:"subnet_id"`
		IPAddress string `json:"ip_address"`
	} `json:"fixed_ips"`
}

func (p rawPort) port() Port {
	out := Port{ID: p.ID, ProjectID: p.ProjectID, NetworkID: p.NetworkID, DeviceID: p.DeviceID, DeviceOwner: p.DeviceOwner}
	for _, ip := range p.FixedIPs {
		if a, err := netip.ParseAddr(ip.IPAddress); err == nil && a.Is4() {
			out.IP = a
			break
		}
	}
	return out
}

type rawSubnet struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	CIDR      string `json:"cidr"`
	IPVersion int    `json:"ip_version"`
}

type rawNetwork struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

func (n *Neutron) GetRouter(ctx context.Context, id string) (Router, error) {
	var r struct {
		Router struct {
			ID        string `json:"id"`
			ProjectID string `json:"project_id"`
		} `json:"router"`
	}
	if err := n.get(ctx, n.sc.ServiceURL("routers", id), &r); err != nil {
		return Router{}, err
	}
	out := Router{ID: r.Router.ID, ProjectID: r.Router.ProjectID}
	var ports struct {
		Ports []rawPort `json:"ports"`
	}
	if err := n.get(ctx, n.query("ports", url.Values{"device_id": {id}}), &ports); err != nil {
		return out, err
	}
	var subnetIDs []string
	for _, p := range ports.Ports {
		if !strings.HasPrefix(p.DeviceOwner, "network:router_interface") && p.DeviceOwner != "network:ha_router_replicated_interface" {
			continue
		}
		for _, ip := range p.FixedIPs {
			subnetIDs = append(subnetIDs, ip.SubnetID)
		}
	}
	// Look subnets up in batches so the query string stays well under proxy URL limits (~8 KB).
	for start := 0; start < len(subnetIDs); start += subnetBatch {
		q := url.Values{"id": subnetIDs[start:min(start+subnetBatch, len(subnetIDs))]}
		var subnets struct {
			Subnets []rawSubnet `json:"subnets"`
		}
		if err := n.get(ctx, n.query("subnets", q), &subnets); err != nil {
			return out, err
		}
		for _, s := range subnets.Subnets {
			if s.IPVersion != 4 || s.ProjectID == n.serviceProjectID {
				continue
			}
			if p, err := netip.ParsePrefix(s.CIDR); err == nil {
				out.SubnetCIDRs = append(out.SubnetCIDRs, p)
			}
		}
	}
	return out, nil
}

func (n *Neutron) CreateNetwork(ctx context.Context, name, tag string) (string, error) {
	var r struct {
		Network rawNetwork `json:"network"`
	}
	body := map[string]any{"network": map[string]any{"name": name, "port_security_enabled": false, "shared": false}}
	if err := n.post(ctx, n.sc.ServiceURL("networks"), body, &r); err != nil {
		return "", err
	}
	return r.Network.ID, n.tagAfterCreate(ctx, "networks", r.Network.ID, tag)
}

func (n *Neutron) CreateSubnet(ctx context.Context, networkID, name, tag string, cidr netip.Prefix) (string, error) {
	var r struct {
		Subnet rawSubnet `json:"subnet"`
	}
	body := map[string]any{"subnet": map[string]any{
		"network_id": networkID, "name": name, "cidr": cidr.String(), "ip_version": 4,
		"gateway_ip": nil, "enable_dhcp": false,
	}}
	if err := n.post(ctx, n.sc.ServiceURL("subnets"), body, &r); err != nil {
		return "", err
	}
	return r.Subnet.ID, n.tagAfterCreate(ctx, "subnets", r.Subnet.ID, tag)
}

func (n *Neutron) CreatePort(ctx context.Context, networkID, subnetID, projectID, name, tag string, ip netip.Addr) (string, error) {
	var r struct {
		Port rawPort `json:"port"`
	}
	body := map[string]any{"port": map[string]any{
		"network_id": networkID, "project_id": projectID, "name": name,
		"fixed_ips": []map[string]string{{"subnet_id": subnetID, "ip_address": ip.String()}},
	}}
	if err := n.post(ctx, n.sc.ServiceURL("ports"), body, &r); err != nil {
		return "", err
	}
	return r.Port.ID, n.tagAfterCreate(ctx, "ports", r.Port.ID, tag)
}

func (n *Neutron) GetPort(ctx context.Context, id string) (Port, error) {
	var r struct {
		Port rawPort `json:"port"`
	}
	if err := n.get(ctx, n.sc.ServiceURL("ports", id), &r); err != nil {
		return Port{}, err
	}
	return r.Port.port(), nil
}

func (n *Neutron) listPorts(ctx context.Context, q url.Values) ([]Port, error) {
	var r struct {
		Ports []rawPort `json:"ports"`
	}
	if err := n.get(ctx, n.query("ports", q), &r); err != nil {
		return nil, err
	}
	out := make([]Port, 0, len(r.Ports))
	for _, p := range r.Ports {
		out = append(out, p.port())
	}
	return out, nil
}

func (n *Neutron) FindPortByIP(ctx context.Context, networkID string, ip netip.Addr) (Port, error) {
	ports, err := n.listPorts(ctx, url.Values{"network_id": {networkID}, "fixed_ips": {"ip_address=" + ip.String()}})
	if err != nil {
		return Port{}, err
	}
	if len(ports) == 0 {
		return Port{}, &APIError{Code: http.StatusNotFound, Type: "PortNotFound", Message: "no port with " + ip.String()}
	}
	return ports[0], nil
}

func (n *Neutron) FindNetworkByName(ctx context.Context, name string) (Network, error) {
	var r struct {
		Networks []rawNetwork `json:"networks"`
	}
	if err := n.get(ctx, n.query("networks", url.Values{"name": {name}, "project_id": {n.serviceProjectID}}), &r); err != nil {
		return Network{}, err
	}
	if len(r.Networks) == 0 {
		return Network{}, &APIError{Code: http.StatusNotFound, Type: "NetworkNotFound", Message: "no network named " + name}
	}
	w := r.Networks[0]
	return Network{ID: w.ID, Name: w.Name, Tags: w.Tags, CreatedAt: w.CreatedAt}, nil
}

func (n *Neutron) FindSubnetByName(ctx context.Context, name string) (string, error) {
	var r struct {
		Subnets []rawSubnet `json:"subnets"`
	}
	if err := n.get(ctx, n.query("subnets", url.Values{"name": {name}, "project_id": {n.serviceProjectID}}), &r); err != nil {
		return "", err
	}
	if len(r.Subnets) == 0 {
		return "", &APIError{Code: http.StatusNotFound, Type: "SubnetNotFound", Message: "no subnet named " + name}
	}
	return r.Subnets[0].ID, nil
}

func (n *Neutron) AddRouterInterface(ctx context.Context, routerID, portID string) error {
	return n.put(ctx, n.sc.ServiceURL("routers", routerID, "add_router_interface"), map[string]string{"port_id": portID}, nil)
}

func (n *Neutron) RemoveRouterInterface(ctx context.Context, routerID, portID string) error {
	return n.put(ctx, n.sc.ServiceURL("routers", routerID, "remove_router_interface"), map[string]string{"port_id": portID}, nil)
}

func (n *Neutron) DeletePort(ctx context.Context, id string) error {
	return n.del(ctx, n.sc.ServiceURL("ports", id))
}

func (n *Neutron) DeleteSubnet(ctx context.Context, id string) error {
	return n.del(ctx, n.sc.ServiceURL("subnets", id))
}

func (n *Neutron) DeleteNetwork(ctx context.Context, id string) error {
	return n.del(ctx, n.sc.ServiceURL("networks", id))
}

func (n *Neutron) ListNetworkPorts(ctx context.Context, networkID string) ([]Port, error) {
	return n.listPorts(ctx, url.Values{"network_id": {networkID}})
}

func (n *Neutron) ListOwnNetworks(ctx context.Context) ([]Network, error) {
	var r struct {
		Networks []rawNetwork `json:"networks"`
	}
	if err := n.get(ctx, n.query("networks", url.Values{"project_id": {n.serviceProjectID}}), &r); err != nil {
		return nil, err
	}
	out := make([]Network, 0, len(r.Networks))
	for _, w := range r.Networks {
		out = append(out, Network{ID: w.ID, Name: w.Name, Tags: w.Tags, CreatedAt: w.CreatedAt})
	}
	return out, nil
}

// ListAllSubnetCIDRs returns every IPv4 subnet in the cloud except the service project's own.
func (n *Neutron) ListAllSubnetCIDRs(ctx context.Context) ([]netip.Prefix, error) {
	var r struct {
		Subnets []rawSubnet `json:"subnets"`
	}
	if err := n.get(ctx, n.query("subnets", url.Values{"ip_version": {"4"}}), &r); err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, s := range r.Subnets {
		if s.ProjectID == n.serviceProjectID {
			continue
		}
		if p, err := netip.ParsePrefix(s.CIDR); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}
