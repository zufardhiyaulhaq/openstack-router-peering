package reconciler

import (
	"context"
	"time"
)

// Run scans the pool once, then runs every job on its own ticker until ctx ends. Job errors are logged, never fatal.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.PoolScanOnce(ctx); err != nil {
		r.log().Error("pool scan", "err", err)
	}
	jobs := []struct {
		name  string
		every time.Duration
		run   func(context.Context) error
	}{
		{"work", r.Cfg.Intervals.Work, r.WorkOnce},
		{"expiry", r.Cfg.Intervals.Expiry, r.ExpireOnce},
		{"drift", r.Cfg.Intervals.Drift, r.DriftOnce},
		{"sweep", r.Cfg.Intervals.Sweep, r.SweepOnce},
		{"pool_scan", r.Cfg.Intervals.PoolScan, r.PoolScanOnce},
	}
	done := make(chan struct{}, len(jobs))
	for _, j := range jobs {
		go func() {
			defer func() { done <- struct{}{} }()
			t := time.NewTicker(j.every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := j.run(ctx); err != nil && ctx.Err() == nil {
						r.log().Error("job failed", "job", j.name, "err", err)
					}
				}
			}
		}()
	}
	for range jobs {
		<-done
	}
	return nil
}
