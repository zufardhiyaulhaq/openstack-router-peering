// Package api is the router peering REST API. It validates Keystone tokens, reads Neutron, and only writes
// Postgres rows; the reconciler does every Neutron write.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/config"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/neutron"
	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/store"
)

// API holds the handlers' dependencies.
type API struct {
	st               *store.Store
	ks               keystone.Client
	nc               neutron.Client
	cfg              config.Config
	serviceProjectID string
}

// Option configures New.
type Option func(*API)

// WithServiceProject makes /readyz also check that Keystone answers for this project.
func WithServiceProject(id string) Option { return func(a *API) { a.serviceProjectID = id } }

// New returns the HTTP handler for every route.
func New(st *store.Store, ks keystone.Client, nc neutron.Client, cfg config.Config, opts ...Option) http.Handler {
	a := &API{st: st, ks: ks, nc: nc, cfg: cfg}
	for _, o := range opts {
		o(a)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /readyz", a.readyz)
	mux.HandleFunc("POST /v1/peerings", a.authed(a.create))
	mux.HandleFunc("GET /v1/peerings", a.authed(a.list))
	mux.HandleFunc("GET /v1/peerings/{id}", a.authed(a.get))
	mux.HandleFunc("GET /v1/peerings/{id}/events", a.authed(a.events))
	a.routeActions(mux)
	return mux
}

type errorBody struct {
	Error    string   `json:"error"`
	Overlaps []string `json:"overlaps,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody{Error: msg})
}

// writeStoreErr maps store errors to HTTP codes.
func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "peering not found")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		slog.Error("store", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}
