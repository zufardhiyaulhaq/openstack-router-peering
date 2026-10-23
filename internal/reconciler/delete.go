package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

// gone treats "already deleted" as success.
func gone(err error) error {
	if neutron.KindOf(err) == neutron.KindNotFound {
		return nil
	}
	return err
}

// removeAll deletes every Neutron object of p (spec §7.3 steps 1-4) and frees its block.
// It returns an error wrapping neutron.ErrInUseByRoute while a user route still uses a next hop.
func (r *Reconciler) removeAll(ctx context.Context, p model.Peering) error {
	// A route on router B towards A uses A's IP as next hop, and the reverse.
	for _, s := range []struct {
		router, port string
		peerIP       netip.Addr
	}{{p.AccepterRouterID, p.AccepterPortID, p.RequesterIP}, {p.RequesterRouterID, p.RequesterPortID, p.AccepterIP}} {
		if s.router == "" || s.port == "" {
			continue
		}
		err := gone(r.Neutron.RemoveRouterInterface(ctx, s.router, s.port))
		if neutron.KindOf(err) == neutron.KindInUseByRoute {
			return &routeBlocked{router: s.router, nexthop: s.peerIP, err: err}
		}
		if err != nil {
			return err
		}
	}
	ports, err := r.Neutron.ListPortsByTag(ctx, model.Tag(p.ID))
	if err != nil {
		return err
	}
	for _, port := range ports {
		if port.DeviceID != "" && port.DeviceOwner != "" {
			if err := gone(r.Neutron.RemoveRouterInterface(ctx, port.DeviceID, port.ID)); err != nil {
				return err
			}
			continue
		}
		if err := gone(r.Neutron.DeletePort(ctx, port.ID)); err != nil {
			return err
		}
	}
	if p.TransitSubnetID != "" {
		if err := gone(r.Neutron.DeleteSubnet(ctx, p.TransitSubnetID)); err != nil {
			return err
		}
	}
	netID := p.TransitNetworkID
	if netID == "" {
		n, err := r.Neutron.FindNetworkByName(ctx, p.ID)
		if gone(err) != nil {
			return err
		}
		netID = n.ID
	}
	if netID != "" {
		if err := gone(r.Neutron.DeleteNetwork(ctx, netID)); err != nil {
			return err
		}
	}
	return r.Store.FreeBlock(ctx, p.ID)
}

// routeBlocked: a user route on router still points at nexthop, so Neutron refuses to remove the interface.
type routeBlocked struct {
	router  string
	nexthop netip.Addr
	err     error
}

func (e *routeBlocked) Error() string {
	return fmt.Sprintf("remove routes via %s on router %s first", e.nexthop, e.router)
}

func (e *routeBlocked) Unwrap() error { return e.err }

// teardown runs spec §7.3 for a peering in deleting. It never gives up: every error is retried with backoff.
func (r *Reconciler) teardown(ctx context.Context, p model.Peering) error {
	err := r.removeAll(ctx, p)
	if err == nil {
		return r.Store.Finish(ctx, p.ID, r.Me, model.StatusDeleting, model.StatusDeleted, store.WorkerPatch{}, model.Event{Action: model.ActionDeleted})
	}
	if errors.Is(err, store.ErrLeaseLost) {
		return err
	}
	var blocked *routeBlocked
	if errors.As(err, &blocked) && p.StatusReason != blocked.Error() {
		ev := &model.Event{Action: model.ActionDeleteBlocked, Detail: map[string]any{"router": blocked.router, "nexthop": blocked.nexthop.String()}}
		if err := r.Store.SaveStep(ctx, p.ID, r.Me, model.StatusDeleting, store.WorkerPatch{}, ev); err != nil {
			return err
		}
	}
	return r.retry(ctx, p, err.Error())
}
