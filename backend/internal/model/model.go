// Package model holds the peering types and the small pure helpers around them.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Status is a peering's lifecycle state.
type Status string

const (
	StatusPendingAcceptance Status = "pending_acceptance"
	StatusProvisioning      Status = "provisioning"
	StatusActive            Status = "active"
	StatusDegraded          Status = "degraded"
	StatusRejected          Status = "rejected"
	StatusCancelled         Status = "cancelled"
	StatusExpired           Status = "expired"
	StatusFailed            Status = "failed"
	StatusDeleting          Status = "deleting"
	StatusDeleted           Status = "deleted"
)

// TerminalStatuses never change again.
var TerminalStatuses = []Status{StatusRejected, StatusCancelled, StatusExpired, StatusFailed, StatusDeleted}

// Terminal reports whether s is a final state.
func (s Status) Terminal() bool {
	for _, t := range TerminalStatuses {
		if s == t {
			return true
		}
	}
	return false
}

// Event actions.
const (
	ActionRequested       = "requested"
	ActionAccepted        = "accepted"
	ActionRejected        = "rejected"
	ActionCancelled       = "cancelled"
	ActionExpired         = "expired"
	ActionProvisionStep   = "provision_step"
	ActionProvisioned     = "provisioned"
	ActionDegraded        = "degraded"
	ActionRepairRequested = "repair_requested"
	ActionRepaired        = "repaired"
	ActionDeleteRequested = "delete_requested"
	ActionDeleteBlocked   = "delete_blocked"
	ActionDeleted         = "deleted"
	ActionProjectGone     = "project_gone"
	ActionRouterGone      = "router_gone"
	ActionError           = "error"
)

// Peering is one row of the peerings table. Empty strings and invalid
// prefixes/addresses stand for NULL.
type Peering struct {
	ID, Name     string
	Status       Status
	StatusReason string

	RequesterProjectID, RequesterRouterID string
	AccepterProjectID, AccepterRouterID   string

	TransitCIDR                                                        netip.Prefix
	TransitNetworkID, TransitSubnetID, RequesterPortID, AccepterPortID string
	RequesterIP, AccepterIP                                            netip.Addr

	Attempts      int
	NextAttemptAt time.Time
	ClaimedBy     string
	ClaimedUntil  time.Time
	Warnings      []string

	CreatedBy, AcceptedBy, DeletedBy           string
	CreatedAt, UpdatedAt, ExpiresAt, DeletedAt time.Time
}

// Event is one audit record.
type Event struct {
	ID                          int64
	PeeringID                   string
	At                          time.Time
	ActorUserID, ActorProjectID string
	Action                      string
	Detail                      map[string]any
}

// NewID returns "rpx-" followed by 12 random lowercase hex characters.
func NewID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "rpx-" + hex.EncodeToString(b)
}

// Tag is the Neutron tag put on every object created for peering id.
func Tag(id string) string { return id }

// ValidProjectID is a loose format check; existence is checked in Keystone.
func ValidProjectID(s string) bool {
	return s != "" && len(s) <= 64 && !strings.ContainsAny(s, " \t\r\n")
}

var routerIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidRouterID is a loose UUID-shape check; existence is checked in Neutron.
func ValidRouterID(s string) bool { return routerIDRe.MatchString(s) }

// BlockIPs returns the requester (first usable) and accepter (second usable) IPs of a block.
func BlockIPs(p netip.Prefix) (requester, accepter netip.Addr) {
	n := p.Masked().Addr()
	requester = n.Next()
	return requester, requester.Next()
}

// Overlaps returns the members of a that overlap any member of b, deduplicated and sorted.
func Overlaps(a, b []netip.Prefix) []netip.Prefix {
	seen := map[netip.Prefix]bool{}
	var out []netip.Prefix
	for _, x := range a {
		for _, y := range b {
			if x.Overlaps(y) && !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if c := out[i].Addr().Compare(out[j].Addr()); c != 0 {
			return c < 0
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out
}
