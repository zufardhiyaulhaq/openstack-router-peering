// Package neutron is the small slice of the Neutron API the service uses.
package neutron

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrInUseByRoute = errors.New("router interface in use by a route")
)

// APIError is a non-2xx Neutron response.
type APIError struct {
	Code    int
	Type    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("neutron %d %s: %s", e.Code, e.Type, e.Message)
}

// Is lets errors.Is match the sentinel errors from a status code / type.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.Code == 404
	case ErrInUseByRoute:
		return e.Code == 409 && e.Type == "RouterInterfaceInUseByRoute"
	}
	return false
}

// Kind classifies an error for retry decisions.
type Kind int

const (
	KindTransient Kind = iota
	KindHard
	KindNotFound
	KindInUseByRoute
)

func (k Kind) String() string {
	return [...]string{"transient", "hard", "not-found", "in-use-by-route"}[k]
}

// KindOf: 404 → NotFound; 409 RouterInterfaceInUseByRoute → InUseByRoute; other 409, 5xx, timeouts and
// connection errors → Transient; any other 4xx → Hard.
func KindOf(err error) Kind {
	switch {
	case err == nil:
		return KindTransient
	case errors.Is(err, ErrNotFound):
		return KindNotFound
	case errors.Is(err, ErrInUseByRoute):
		return KindInUseByRoute
	}
	var api *APIError
	if errors.As(err, &api) && api.Code >= 400 && api.Code < 500 && api.Code != 409 && api.Code != 429 {
		return KindHard
	}
	return KindTransient
}

// Router is a router with the IPv4 subnets on its interfaces (transit rpx-* subnets excluded).
type Router struct {
	ID, ProjectID string
	SubnetCIDRs   []netip.Prefix
}

// Port is the subset of a Neutron port the service reads.
type Port struct {
	ID, ProjectID, NetworkID, DeviceID, DeviceOwner string
	IP                                              netip.Addr
}

// Network is the subset of a Neutron network the service reads.
type Network struct {
	ID, Name  string
	Tags      []string
	CreatedAt time.Time
}

// Client is every Neutron call the service makes. Only the reconciler may call the write methods.
type Client interface {
	GetRouter(ctx context.Context, id string) (Router, error)
	CreateNetwork(ctx context.Context, name, tag string) (string, error)
	CreateSubnet(ctx context.Context, networkID, name, tag string, cidr netip.Prefix) (string, error)
	CreatePort(ctx context.Context, networkID, subnetID, projectID, name, tag string, ip netip.Addr) (string, error)
	GetPort(ctx context.Context, id string) (Port, error)
	FindPortByIP(ctx context.Context, networkID string, ip netip.Addr) (Port, error)
	FindNetworkByName(ctx context.Context, name string) (Network, error)
	FindSubnetByName(ctx context.Context, name string) (string, error)
	AddRouterInterface(ctx context.Context, routerID, portID string) error
	RemoveRouterInterface(ctx context.Context, routerID, portID string) error
	DeletePort(ctx context.Context, id string) error
	DeleteSubnet(ctx context.Context, id string) error
	DeleteNetwork(ctx context.Context, id string) error
	ListPortsByTag(ctx context.Context, tag string) ([]Port, error)
	ListOwnNetworks(ctx context.Context) ([]Network, error)
	ListAllSubnetCIDRs(ctx context.Context) ([]netip.Prefix, error)
}

// TransitPrefix is the name prefix of every object the service creates.
const TransitPrefix = "rpx-"
