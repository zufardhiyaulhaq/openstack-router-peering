package keystone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

func newKeystone(t *testing.T, h http.HandlerFunc) *Keystone {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewKeystone(&gophercloud.ServiceClient{ProviderClient: &gophercloud.ProviderClient{TokenID: "svc"}, Endpoint: srv.URL + "/v3/"})
}

func TestKeystoneValidate(t *testing.T) {
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	k := newKeystone(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/auth/tokens" || r.Header.Get("X-Subject-Token") == "" || r.Header.Get("X-Auth-Token") != "svc" {
			t.Errorf("bad request %s %v", r.URL.Path, r.Header)
		}
		if r.Header.Get("X-Subject-Token") == "bad" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{
			"expires_at": exp.Format(time.RFC3339),
			"user":       map[string]any{"id": "u1"},
			"project":    map[string]any{"id": "pa", "name": "team-a", "domain": map[string]any{"id": "default"}},
			"roles":      []map[string]any{{"name": "member"}, {"name": "reader"}},
		}})
	})
	tok, err := k.Validate(context.Background(), "good")
	if err != nil {
		t.Fatal(err)
	}
	if tok.UserID != "u1" || tok.ProjectID != "pa" || tok.ProjectName != "team-a" || tok.DomainID != "default" ||
		len(tok.Roles) != 2 || !tok.ExpiresAt.Equal(exp) {
		t.Fatalf("token %+v", tok)
	}
	if _, err := k.Validate(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestKeystoneGetProject(t *testing.T) {
	k := newKeystone(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/projects/missing" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": "pa", "name": "team-a", "domain_id": "default", "enabled": false}})
	})
	p, err := k.GetProject(context.Background(), "pa")
	if err != nil || p.Name != "team-a" || p.Enabled || p.DomainID != "default" {
		t.Fatalf("project %+v err %v", p, err)
	}
	if _, err := k.GetProject(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

type countingClient struct {
	n   atomic.Int32
	exp time.Time
}

func (c *countingClient) Validate(context.Context, string) (Token, error) {
	c.n.Add(1)
	return Token{UserID: "u", ProjectID: "p", ExpiresAt: c.exp}, nil
}
func (c *countingClient) GetProject(context.Context, string) (Project, error) { return Project{}, nil }

func TestCacheValidate(t *testing.T) {
	inner := &countingClient{exp: time.Now().Add(time.Hour)}
	c := NewCached(inner, time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := c.Validate(context.Background(), "t1"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.n.Load() != 1 {
		t.Fatalf("inner called %d times", inner.n.Load())
	}
	short := &countingClient{exp: time.Now().Add(10 * time.Millisecond)}
	c2 := NewCached(short, time.Minute)
	_, _ = c2.Validate(context.Background(), "t2")
	time.Sleep(20 * time.Millisecond)
	_, _ = c2.Validate(context.Background(), "t2")
	if short.n.Load() != 2 {
		t.Fatalf("expired token must be revalidated, inner called %d", short.n.Load())
	}
}
