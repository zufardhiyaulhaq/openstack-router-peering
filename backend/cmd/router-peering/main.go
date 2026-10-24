// Command router-peering runs the router peering API, the reconciler, or the migrations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/api"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/openstack"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/reconciler"
	"github.com/zufardhiyaulhaq/openstack-router-peering/backend/internal/store"
)

const usage = "usage: router-peering <api|reconciler|migrate> --config <path>"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/router-peering/router-peering.yaml", "config file")
	_ = fs.Parse(os.Args[2:])

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("cmd", cmd))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "api", "reconciler", "migrate":
		err = run(ctx, cmd, *cfgPath)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if cmd == "migrate" {
		slog.Info("migrations applied")
		return nil
	}

	clients, err := openstack.Connect(ctx, cfg.Keystone)
	if err != nil {
		return err
	}
	ks := keystone.NewKeystone(clients.Identity)
	nc, err := neutron.NewNeutron(ctx, clients.Network, clients.ServiceProjectID)
	if err != nil {
		return fmt.Errorf("neutron: %w", err)
	}

	if cmd == "reconciler" {
		host, _ := os.Hostname()
		r := &reconciler.Reconciler{Store: st, Neutron: nc, Keystone: ks, Cfg: cfg, Me: fmt.Sprintf("%s-%d", host, os.Getpid())}
		slog.Info("reconciler started", "me", r.Me)
		return r.Run(ctx)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(st, keystone.NewCached(ks, cfg.TokenCacheTTL), nc, cfg, api.WithServiceProject(clients.ServiceProjectID)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("api listening", "addr", cfg.Listen)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
