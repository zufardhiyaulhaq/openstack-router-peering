// Package fake is an in-memory neutron.Client for tests.
package fake

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
)

type router struct {
	id, project string
	cidrs       []netip.Prefix // tenant subnets (never rpx-*)
	routes      map[netip.Addr]bool
}

type subnet struct {
	id, network, name, tag, project string
	cidr                            netip.Prefix
}

type network struct {
	neutron.Network
	project string
}

type port struct {
	neutron.Port
	subnet, name, tag string
}

// Fake keeps routers, networks, subnets and ports in maps.
type Fake struct {
	mu       sync.Mutex
	seq      int
	routers  map[string]*router
	networks map[string]*network
	subnets  map[string]*subnet
	ports    map[string]*port
	fail     map[string][]error
	// Now is the clock for network CreatedAt; defaults to time.Now.
	Now func() time.Time
	// OnCall, if set, runs at the start of every call that supports FailNext (under the fake's lock).
	OnCall func(method string)
}

var _ neutron.Client = (*Fake)(nil)

// New returns an empty fake.
func New() *Fake {
	return &Fake{routers: map[string]*router{}, networks: map[string]*network{}, subnets: map[string]*subnet{},
		ports: map[string]*port{}, fail: map[string][]error{}, Now: time.Now}
}

func (f *Fake) id(kind string) string {
	f.seq++
	code := map[string]int{"network": 1, "subnet": 2, "port": 3}[kind]
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", code, f.seq) // valid UUIDs: the DB stores them in uuid columns
}

func notFound(kind, id string) error {
	return &neutron.APIError{Code: 404, Type: kind + "NotFound", Message: kind + " " + id + " could not be found"}
}

// FailNext makes the next call to method return err (once per call to FailNext).
func (f *Fake) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[method] = append(f.fail[method], err)
}

func (f *Fake) injected(method string) error {
	if f.OnCall != nil {
		f.OnCall(method)
	}
	if q := f.fail[method]; len(q) > 0 {
		f.fail[method] = q[1:]
		return q[0]
	}
	return nil
}

// AddRouter registers a router owned by project with tenant subnets cidrs.
func (f *Fake) AddRouter(id, project string, cidrs ...netip.Prefix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routers[id] = &router{id: id, project: project, cidrs: cidrs, routes: map[netip.Addr]bool{}}
}

// DeleteRouter removes a router.
func (f *Fake) DeleteRouter(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routers, id)
}

// AddRouterSubnet adds a tenant subnet to a router.
func (f *Fake) AddRouterSubnet(id string, cidr netip.Prefix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routers[id].cidrs = append(f.routers[id].cidrs, cidr)
}

// SetRoute adds a static route on router via nexthop. Like Neutron, it blocks removing the router
// interface whose subnet contains nexthop.
func (f *Fake) SetRoute(routerID string, nexthop netip.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routers[routerID].routes[nexthop] = true
}

// ClearRoutes removes every static route on router.
func (f *Fake) ClearRoutes(routerID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routers[routerID].routes = map[netip.Addr]bool{}
}

// AddNetwork creates a network directly (e.g. an orphan) with a given creation time.
func (f *Fake) AddNetwork(name string, created time.Time) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id("network")
	f.networks[id] = &network{Network: neutron.Network{ID: id, Name: name, Tags: []string{name}, CreatedAt: created}, project: "svc"}
	return id
}

// svcProject owns everything the service creates.
const svcProject = "svc"

// AddTenantNetwork creates a network owned by a tenant project (e.g. a lookalike named rpx-<id>).
func (f *Fake) AddTenantNetwork(name, project string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id("network")
	f.networks[id] = &network{Network: neutron.Network{ID: id, Name: name, CreatedAt: f.Now()}, project: project}
	return id
}

// AddTenantSubnet creates a subnet owned by a tenant project.
func (f *Fake) AddTenantSubnet(networkID, name, project string, cidr netip.Prefix) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.id("subnet")
	f.subnets[id] = &subnet{id: id, network: networkID, name: name, project: project, cidr: cidr}
	return id
}

// HasNetwork reports whether a network exists.
func (f *Fake) HasNetwork(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.networks[id]
	return ok
}

// Counts returns the number of networks, subnets and ports owned by the service project.
func (f *Fake) Counts() (networks, subnets, ports int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.networks {
		if n.project == svcProject {
			networks++
		}
	}
	for _, s := range f.subnets {
		if s.project == svcProject {
			subnets++
		}
	}
	return networks, subnets, len(f.ports)
}

// RemovePortDirect deletes a port as a tenant would with remove_router_interface.
func (f *Fake) RemovePortDirect(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.ports, id)
}

func (f *Fake) GetRouter(_ context.Context, id string) (neutron.Router, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("GetRouter"); err != nil {
		return neutron.Router{}, err
	}
	r, ok := f.routers[id]
	if !ok {
		return neutron.Router{}, notFound("Router", id)
	}
	return neutron.Router{ID: r.id, ProjectID: r.project, SubnetCIDRs: append([]netip.Prefix(nil), r.cidrs...)}, nil
}

func (f *Fake) CreateNetwork(_ context.Context, name, tag string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("CreateNetwork"); err != nil {
		return "", err
	}
	id := f.id("network")
	f.networks[id] = &network{Network: neutron.Network{ID: id, Name: name, Tags: []string{tag}, CreatedAt: f.Now()}, project: "svc"}
	return id, nil
}

func (f *Fake) CreateSubnet(_ context.Context, networkID, name, tag string, cidr netip.Prefix) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("CreateSubnet"); err != nil {
		return "", err
	}
	if _, ok := f.networks[networkID]; !ok {
		return "", notFound("Network", networkID)
	}
	id := f.id("subnet")
	f.subnets[id] = &subnet{id: id, network: networkID, name: name, tag: tag, cidr: cidr, project: svcProject}
	return id, nil
}

func (f *Fake) CreatePort(_ context.Context, networkID, subnetID, projectID, name, tag string, ip netip.Addr) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("CreatePort"); err != nil {
		return "", err
	}
	if _, ok := f.subnets[subnetID]; !ok {
		return "", notFound("Subnet", subnetID)
	}
	for _, p := range f.ports {
		if p.NetworkID == networkID && p.IP == ip {
			return "", &neutron.APIError{Code: 409, Type: "IpAddressAlreadyAllocated", Message: ip.String() + " already allocated"}
		}
	}
	id := f.id("port")
	f.ports[id] = &port{Port: neutron.Port{ID: id, ProjectID: projectID, NetworkID: networkID, IP: ip}, subnet: subnetID, name: name, tag: tag}
	return id, nil
}

func (f *Fake) GetPort(_ context.Context, id string) (neutron.Port, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("GetPort"); err != nil {
		return neutron.Port{}, err
	}
	p, ok := f.ports[id]
	if !ok {
		return neutron.Port{}, notFound("Port", id)
	}
	return p.Port, nil
}

func (f *Fake) FindPortByIP(_ context.Context, networkID string, ip netip.Addr) (neutron.Port, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.ports {
		if p.NetworkID == networkID && p.IP == ip {
			return p.Port, nil
		}
	}
	return neutron.Port{}, notFound("Port", ip.String())
}

func (f *Fake) FindNetworkByName(_ context.Context, name string) (neutron.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.networks {
		if n.Name == name && n.project == svcProject {
			return n.Network, nil
		}
	}
	return neutron.Network{}, notFound("Network", name)
}

func (f *Fake) FindSubnetByName(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.subnets {
		if s.name == name && s.project == svcProject {
			return s.id, nil
		}
	}
	return "", notFound("Subnet", name)
}

func (f *Fake) AddRouterInterface(_ context.Context, routerID, portID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("AddRouterInterface"); err != nil {
		return err
	}
	r, ok := f.routers[routerID]
	if !ok {
		return notFound("Router", routerID)
	}
	p, ok := f.ports[portID]
	if !ok {
		return notFound("Port", portID)
	}
	if p.DeviceID == routerID {
		return nil
	}
	if p.DeviceID != "" {
		return &neutron.APIError{Code: 409, Type: "PortInUse", Message: "port in use"}
	}
	cidr := f.subnets[p.subnet].cidr
	for _, c := range r.cidrs {
		if c.Overlaps(cidr) {
			return &neutron.APIError{Code: 400, Type: "BadRequest", Message: fmt.Sprintf("Bad router request: Cidr %s of subnet overlaps with cidr %s", cidr, c)}
		}
	}
	p.DeviceID, p.DeviceOwner = routerID, "network:router_interface"
	return nil
}

func (f *Fake) RemoveRouterInterface(_ context.Context, routerID, portID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("RemoveRouterInterface"); err != nil {
		return err
	}
	r, ok := f.routers[routerID]
	if !ok {
		return notFound("Router", routerID)
	}
	p, ok := f.ports[portID]
	if !ok || p.DeviceID != routerID {
		return &neutron.APIError{Code: 404, Type: "RouterInterfaceNotFound", Message: "Router " + routerID + " does not have an interface with id " + portID}
	}
	cidr := f.subnets[p.subnet].cidr
	for nexthop := range r.routes {
		if !cidr.Contains(nexthop) {
			continue
		}
		return &neutron.APIError{Code: 409, Type: "RouterInterfaceInUseByRoute", Message: fmt.Sprintf("Router interface for subnet %s on router %s cannot be deleted, as it is required by one or more routes.", p.subnet, routerID)}
	}
	delete(f.ports, portID)
	return nil
}

func (f *Fake) DeletePort(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("DeletePort"); err != nil {
		return err
	}
	if _, ok := f.ports[id]; !ok {
		return notFound("Port", id)
	}
	delete(f.ports, id)
	return nil
}

func (f *Fake) DeleteSubnet(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("DeleteSubnet"); err != nil {
		return err
	}
	if _, ok := f.subnets[id]; !ok {
		return notFound("Subnet", id)
	}
	for _, p := range f.ports {
		if p.subnet == id {
			return &neutron.APIError{Code: 409, Type: "SubnetInUse", Message: "subnet in use"}
		}
	}
	delete(f.subnets, id)
	return nil
}

func (f *Fake) DeleteNetwork(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.injected("DeleteNetwork"); err != nil {
		return err
	}
	if _, ok := f.networks[id]; !ok {
		return notFound("Network", id)
	}
	for _, s := range f.subnets {
		if s.network == id {
			delete(f.subnets, s.id) // Neutron deletes a network's unused subnets with it
		}
	}
	delete(f.networks, id)
	return nil
}

func (f *Fake) ListNetworkPorts(_ context.Context, networkID string) ([]neutron.Port, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []neutron.Port
	for _, p := range f.ports {
		if p.NetworkID == networkID {
			out = append(out, p.Port)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) ListOwnNetworks(_ context.Context) ([]neutron.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []neutron.Network
	for _, n := range f.networks {
		if n.project == svcProject {
			out = append(out, n.Network)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *Fake) ListAllSubnetCIDRs(_ context.Context) ([]netip.Prefix, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []netip.Prefix
	for _, r := range f.routers {
		out = append(out, r.cidrs...)
	}
	for _, s := range f.subnets {
		if s.project != svcProject {
			out = append(out, s.cidr)
		}
	}
	return out, nil
}
