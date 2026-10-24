// Package keystone validates user tokens and looks up projects.
package keystone

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnauthorized = errors.New("invalid or expired token")
	ErrNotFound     = errors.New("project not found")
)

// Token is a validated user token. ProjectID is empty for unscoped tokens.
type Token struct {
	UserID, ProjectID, ProjectName, DomainID string
	Roles                                    []string
	ExpiresAt                                time.Time
}

// HasRole reports whether role is in the token (Keystone already includes implied roles).
func (t Token) HasRole(role string) bool {
	for _, r := range t.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Project is a Keystone project.
type Project struct {
	ID, Name, DomainID string
	Enabled            bool
}

// Client is every Keystone call the service makes.
type Client interface {
	Validate(ctx context.Context, token string) (Token, error)
	GetProject(ctx context.Context, id string) (Project, error)
}
