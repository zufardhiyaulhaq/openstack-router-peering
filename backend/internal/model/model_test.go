package model

import (
	"net/netip"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestNewIDFormat(t *testing.T) {
	re := regexp.MustCompile(`^rpx-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !re.MatchString(id) {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestTerminal(t *testing.T) {
	terminal := map[Status]bool{StatusRejected: true, StatusCancelled: true, StatusExpired: true, StatusFailed: true, StatusDeleted: true}
	all := []Status{StatusPendingAcceptance, StatusProvisioning, StatusActive, StatusDegraded, StatusRejected,
		StatusCancelled, StatusExpired, StatusFailed, StatusDeleting, StatusDeleted}
	for _, s := range all {
		if s.Terminal() != terminal[s] {
			t.Errorf("%s.Terminal() = %v", s, s.Terminal())
		}
	}
	if StatusPendingAcceptance != "pending_acceptance" {
		t.Errorf("status string = %q", StatusPendingAcceptance)
	}
}

func TestBlockIPs(t *testing.T) {
	r, a := BlockIPs(netip.MustParsePrefix("100.64.0.8/29"))
	if r != netip.MustParseAddr("100.64.0.9") || a != netip.MustParseAddr("100.64.0.10") {
		t.Fatalf("got %v %v", r, a)
	}
}

func TestOverlaps(t *testing.T) {
	p := netip.MustParsePrefix
	got := Overlaps([]netip.Prefix{p("198.51.100.0/24"), p("10.0.0.0/24"), p("10.0.0.0/24")}, []netip.Prefix{p("10.0.0.128/25")})
	if !reflect.DeepEqual(got, []netip.Prefix{p("10.0.0.0/24")}) {
		t.Fatalf("got %v", got)
	}
	if got := Overlaps([]netip.Prefix{p("10.1.0.0/24")}, []netip.Prefix{p("10.2.0.0/24")}); len(got) != 0 {
		t.Fatalf("expected none, got %v", got)
	}
}

func TestValidProjectID(t *testing.T) {
	if !ValidProjectID("8f3c1a9e2b7d4e6f9a0b1c2d3e4f5a6b") {
		t.Error("valid id rejected")
	}
	for _, bad := range []string{"", strings.Repeat("a", 65), "a b"} {
		if ValidProjectID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTag(t *testing.T) {
	if Tag("rpx-abc") != "rpx-abc" {
		t.Fatal("tag must equal id")
	}
}
