package store

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
)

// ClaimWork leases up to limit due provisioning/deleting peerings to me, skipping the IDs in skip.
func (s *Store) ClaimWork(ctx context.Context, me string, limit int, lease time.Duration, skip ...string) ([]model.Peering, error) {
	if skip == nil {
		skip = []string{}
	}
	rows, err := s.pool.Query(ctx, `UPDATE peerings SET claimed_by = $1, claimed_until = now() + $3::interval
		WHERE id IN (SELECT id FROM peerings
		             WHERE status IN ('provisioning', 'deleting') AND next_attempt_at <= now()
		               AND (claimed_until IS NULL OR claimed_until < now()) AND NOT (id = ANY($4::text[]))
		             ORDER BY next_attempt_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+peeringCols, me, limit, lease, skip)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

// ExtendLease pushes the lease end out; ErrLeaseLost if me no longer holds it.
func (s *Store) ExtendLease(ctx context.Context, id, me string, lease time.Duration) error {
	tag, err := s.pool.Exec(ctx, `UPDATE peerings SET claimed_until = now() + $3::interval WHERE id = $1 AND claimed_by = $2`, id, me, lease)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// WorkerPatch lists the columns a worker may set; nil means unchanged.
type WorkerPatch struct {
	StatusReason                                                       *string
	TransitCIDR                                                        *netip.Prefix
	RequesterIP, AccepterIP                                            *netip.Addr
	TransitNetworkID, TransitSubnetID, RequesterPortID, AccepterPortID *string
}

func (w WorkerPatch) apply(sets []string, args []any) ([]string, []any) {
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if w.StatusReason != nil {
		set("status_reason", nullStr(*w.StatusReason))
	}
	if w.TransitCIDR != nil {
		if w.TransitCIDR.IsValid() {
			set("transit_cidr", *w.TransitCIDR)
		} else {
			set("transit_cidr", nil)
		}
	}
	addr := func(col string, a *netip.Addr) {
		if a == nil {
			return
		}
		if a.IsValid() {
			set(col, *a)
		} else {
			set(col, nil)
		}
	}
	addr("requester_ip", w.RequesterIP)
	addr("accepter_ip", w.AccepterIP)
	for col, v := range map[string]*string{"transit_network_id": w.TransitNetworkID, "transit_subnet_id": w.TransitSubnetID,
		"requester_port_id": w.RequesterPortID, "accepter_port_id": w.AccepterPortID} {
		if v != nil {
			set(col, nullStr(*v))
		}
	}
	return sets, args
}

// guardArgs are the first 3 parameters of every worker UPDATE: $1 id, $2 claimed_by, $3 expected status.
func guardArgs(id, me string, expect model.Status) []any { return []any{id, me, string(expect)} }

// workerUpdate runs one UPDATE guarded by claimed_by = me and status = expect, plus an optional event, in one transaction.
func (s *Store) workerUpdate(ctx context.Context, id string, sets []string, args []any, ev *model.Event) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, "UPDATE peerings SET "+strings.Join(append(sets, "updated_at = now()"), ", ")+
		" WHERE id = $1 AND claimed_by = $2 AND status::text = $3", args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	if ev != nil {
		ev.PeeringID = id
		if err := addEvent(ctx, tx, *ev); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SaveStep records progress on a claimed peering; ErrLeaseLost if me no longer owns it in status expect.
func (s *Store) SaveStep(ctx context.Context, id, me string, expect model.Status, patch WorkerPatch, ev *model.Event) error {
	sets, args := patch.apply(nil, guardArgs(id, me, expect))
	return s.workerUpdate(ctx, id, sets, args, ev)
}

// Finish moves a claimed peering to `to`, clears the claim and attempts, and records ev.
func (s *Store) Finish(ctx context.Context, id, me string, expect, to model.Status, patch WorkerPatch, ev model.Event) error {
	if patch.StatusReason == nil {
		empty := ""
		patch.StatusReason = &empty
	}
	sets, args := patch.apply(nil, guardArgs(id, me, expect))
	args = append(args, string(to))
	sets = append(sets, fmt.Sprintf("status = $%d::text::peering_status", len(args)),
		"claimed_by = NULL", "claimed_until = NULL", "attempts = 0")
	if to == model.StatusDeleted {
		sets = append(sets, "deleted_at = now()")
	}
	return s.workerUpdate(ctx, id, sets, args, &ev)
}

// Retry releases a claimed peering for another attempt at next, with reason.
func (s *Store) Retry(ctx context.Context, id, me string, expect model.Status, reason string, next time.Time) error {
	sets := []string{"attempts = attempts + 1", "next_attempt_at = $4", "status_reason = $5", "claimed_by = NULL", "claimed_until = NULL"}
	return s.workerUpdate(ctx, id, sets, append(guardArgs(id, me, expect), next, nullStr(reason)), nil)
}
