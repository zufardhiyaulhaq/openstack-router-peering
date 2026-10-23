// Package openstack authenticates the service account and builds the Keystone and Neutron clients.
package openstack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	gopenstack "github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/config"
)

// Clients are the authenticated service clients plus the service project's ID.
type Clients struct {
	Identity, Network *gophercloud.ServiceClient
	ServiceProjectID  string
}

// RequestTimeout bounds every Keystone and Neutron call. It must stay well under the reconciler lease.
const RequestTimeout = 30 * time.Second

// Connect logs in as the service account (re-authenticating when the token expires).
func Connect(ctx context.Context, k config.KeystoneConfig) (Clients, error) {
	provider, err := gopenstack.NewClient(k.AuthURL)
	if err != nil {
		return Clients{}, err
	}
	provider.HTTPClient = http.Client{Timeout: RequestTimeout}
	err = gopenstack.Authenticate(ctx, provider, gophercloud.AuthOptions{
		IdentityEndpoint: k.AuthURL,
		Username:         k.Username,
		Password:         k.Password,
		DomainName:       k.UserDomainName,
		AllowReauth:      true,
		Scope:            &gophercloud.AuthScope{ProjectName: k.ProjectName, DomainName: k.ProjectDomainName},
	})
	if err != nil {
		return Clients{}, fmt.Errorf("keystone login as %s: %w", k.Username, err)
	}
	var c Clients
	res, ok := provider.GetAuthResult().(tokens.CreateResult)
	if !ok {
		return c, errors.New("unexpected keystone auth result")
	}
	project, err := res.ExtractProject()
	if err != nil || project == nil {
		return c, fmt.Errorf("service token has no project scope: %v", err)
	}
	c.ServiceProjectID = project.ID
	eo := gophercloud.EndpointOpts{Region: k.Region, Availability: gophercloud.Availability(k.Interface)}
	if c.Identity, err = gopenstack.NewIdentityV3(provider, eo); err != nil {
		return c, err
	}
	if c.Network, err = gopenstack.NewNetworkV2(provider, eo); err != nil {
		return c, err
	}
	return c, nil
}
