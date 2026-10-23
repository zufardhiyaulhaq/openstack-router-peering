// Package reconciler does every Neutron write: provision, repair, delete and the periodic jobs.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

// Reconciler holds the dependencies of every job. Now defaults to time.Now; Log to slog.Default.
type Reconciler struct {
	Store    *store.Store
	Neutron  neutron.Client
	Keystone keystone.Client
	Cfg      config.Config
	Me       string
	Now      func() time.Time
	Log      *slog.Logger
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Backoff is initial * 2^attempts, capped at max.
func Backoff(attempts int, initial, max time.Duration) time.Duration {
	d := initial
	for i := 0; i < attempts && d < max; i++ {
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}

// hardError marks a failure that must not be retried.
type hardError struct{ msg string }

func (e *hardError) Error() string { return e.msg }

func hard(format string, a ...any) error { return &hardError{msg: fmt.Sprintf(format, a...)} }

func isHard(err error) bool {
	var h *hardError
	if errors.As(err, &h) {
		return true
	}
	k := neutron.KindOf(err)
	return k == neutron.KindHard || k == neutron.KindNotFound
}

// WorkOnce claims due peerings and processes each.
func (r *Reconciler) WorkOnce(ctx context.Context) error {
	ps, err := r.Store.ClaimWork(ctx, r.Me, 10, r.Cfg.Lease)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if err := r.Process(ctx, p); err != nil {
			r.log().Error("process peering", "id", p.ID, "status", p.Status, "err", err)
		}
	}
	return nil
}

// Process runs the work for a claimed peering's status.
func (r *Reconciler) Process(ctx context.Context, p model.Peering) error {
	var err error
	switch p.Status {
	case model.StatusProvisioning:
		err = r.provision(ctx, p)
	case model.StatusDeleting:
		err = r.teardown(ctx, p)
	default:
		return nil
	}
	if errors.Is(err, store.ErrLeaseLost) {
		return nil // someone else owns the row now (e.g. the API moved it to deleting)
	}
	return err
}

// retry releases the claim for a later attempt.
func (r *Reconciler) retry(ctx context.Context, p model.Peering, reason string) error {
	next := r.now().Add(Backoff(p.Attempts, r.Cfg.Backoff.Initial, r.Cfg.Backoff.Max))
	return r.Store.Retry(ctx, p.ID, r.Me, p.Status, reason, next)
}

// step saves progress and extends the lease.
func (r *Reconciler) step(ctx context.Context, p model.Peering, name string, patch store.WorkerPatch) error {
	ev := &model.Event{Action: model.ActionProvisionStep, Detail: map[string]any{"step": name}}
	if err := r.Store.SaveStep(ctx, p.ID, r.Me, p.Status, patch, ev); err != nil {
		return err
	}
	return r.Store.ExtendLease(ctx, p.ID, r.Me, r.Cfg.Lease)
}
