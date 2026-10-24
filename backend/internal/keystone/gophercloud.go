package keystone

import (
	"context"
	"errors"
	"time"

	"github.com/gophercloud/gophercloud/v2"
)

// Keystone implements Client over a gophercloud identity v3 ServiceClient (authenticated as the service account).
type Keystone struct{ sc *gophercloud.ServiceClient }

var _ Client = (*Keystone)(nil)

// NewKeystone wraps an identity v3 client.
func NewKeystone(sc *gophercloud.ServiceClient) *Keystone { return &Keystone{sc: sc} }

func is4xx(err error, codes ...int) bool {
	var uc gophercloud.ErrUnexpectedResponseCode
	if !errors.As(err, &uc) {
		return false
	}
	for _, c := range codes {
		if uc.Actual == c {
			return true
		}
	}
	return false
}

func (k *Keystone) Validate(ctx context.Context, token string) (Token, error) {
	var r struct {
		Token struct {
			ExpiresAt time.Time `json:"expires_at"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
			Project *struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Domain struct {
					ID string `json:"id"`
				} `json:"domain"`
			} `json:"project"`
			Roles []struct {
				Name string `json:"name"`
			} `json:"roles"`
		} `json:"token"`
	}
	_, err := k.sc.Get(ctx, k.sc.ServiceURL("auth", "tokens"), &r, &gophercloud.RequestOpts{
		OkCodes: []int{200}, MoreHeaders: map[string]string{"X-Subject-Token": token},
	})
	if is4xx(err, 401, 403, 404) {
		return Token{}, ErrUnauthorized
	}
	if err != nil {
		return Token{}, err
	}
	t := Token{UserID: r.Token.User.ID, ExpiresAt: r.Token.ExpiresAt}
	if p := r.Token.Project; p != nil {
		t.ProjectID, t.ProjectName, t.DomainID = p.ID, p.Name, p.Domain.ID
	}
	for _, role := range r.Token.Roles {
		t.Roles = append(t.Roles, role.Name)
	}
	return t, nil
}

func (k *Keystone) GetProject(ctx context.Context, id string) (Project, error) {
	var r struct {
		Project struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			DomainID string `json:"domain_id"`
			Enabled  bool   `json:"enabled"`
		} `json:"project"`
	}
	_, err := k.sc.Get(ctx, k.sc.ServiceURL("projects", id), &r, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if is4xx(err, 404) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	p := r.Project
	return Project{ID: p.ID, Name: p.Name, DomainID: p.DomainID, Enabled: p.Enabled}, nil
}
