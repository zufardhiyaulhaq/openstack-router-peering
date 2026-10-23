package reconciler

import (
	"context"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
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
	for _, s := range []struct{ router, port string }{{p.AccepterRouterID, p.AccepterPortID}, {p.RequesterRouterID, p.RequesterPortID}} {
		if s.router == "" || s.port == "" {
			continue
		}
		if err := gone(r.Neutron.RemoveRouterInterface(ctx, s.router, s.port)); err != nil {
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

// teardown is implemented in Task 9.
func (r *Reconciler) teardown(ctx context.Context, p model.Peering) error { return nil }
