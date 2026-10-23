// Package fake is an in-memory keystone.Client for tests.
package fake

import (
	"context"
	"sync"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/keystone"
)

// Fake maps tokens and projects.
type Fake struct {
	mu       sync.Mutex
	tokens   map[string]keystone.Token
	projects map[string]keystone.Project
	// FailGetProject, when set, is returned by GetProject.
	FailGetProject error
}

var _ keystone.Client = (*Fake)(nil)

// New returns an empty fake.
func New() *Fake {
	return &Fake{tokens: map[string]keystone.Token{}, projects: map[string]keystone.Project{}}
}

// AddToken registers a valid token.
func (f *Fake) AddToken(token string, t keystone.Token) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = t
}

// AddProject registers or replaces a project.
func (f *Fake) AddProject(p keystone.Project) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[p.ID] = p
}

// DeleteProject removes a project.
func (f *Fake) DeleteProject(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.projects, id)
}

func (f *Fake) Validate(_ context.Context, token string) (keystone.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[token]
	if !ok {
		return keystone.Token{}, keystone.ErrUnauthorized
	}
	return t, nil
}

func (f *Fake) GetProject(_ context.Context, id string) (keystone.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailGetProject != nil {
		return keystone.Project{}, f.FailGetProject
	}
	p, ok := f.projects[id]
	if !ok {
		return keystone.Project{}, keystone.ErrNotFound
	}
	return p, nil
}
