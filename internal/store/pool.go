package store

import (
	"context"
	"errors"
	"net/netip"

	"github.com/jackc/pgx/v5"
)

// blocks splits pool into /29 prefixes.
func blocks(pool netip.Prefix) []string {
	pool = pool.Masked()
	var out []string
	for a := pool.Addr(); pool.Contains(a); {
		p := netip.PrefixFrom(a, 29)
		out = append(out, p.String())
		for i := 0; i < 8; i++ {
			a = a.Next()
		}
	}
	return out
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Masked().String())
	}
	return out
}

// SeedBlocks inserts every /29 of pool that is not there yet.
func (s *Store) SeedBlocks(ctx context.Context, pool netip.Prefix) (int, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO transit_blocks (cidr) SELECT unnest($1::cidr[]) ON CONFLICT DO NOTHING`, blocks(pool))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// AllocateBlock returns the block held by peeringID, or takes the lowest free unreserved one.
func (s *Store) AllocateBlock(ctx context.Context, peeringID string) (netip.Prefix, error) {
	var p netip.Prefix
	err := s.pool.QueryRow(ctx, `SELECT cidr FROM transit_blocks WHERE peering_id = $1`, peeringID).Scan(&p)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	err = s.pool.QueryRow(ctx, `UPDATE transit_blocks SET peering_id = $1
		WHERE cidr = (SELECT cidr FROM transit_blocks WHERE peering_id IS NULL AND NOT reserved
		              ORDER BY cidr LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING cidr`, peeringID).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrPoolExhausted
	}
	return p, err
}

// FreeBlock returns peeringID's block to the pool.
func (s *Store) FreeBlock(ctx context.Context, peeringID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE transit_blocks SET peering_id = NULL WHERE peering_id = $1`, peeringID)
	return err
}

// SetReserved marks free blocks overlapping any of used as reserved and clears the rest. Held blocks are untouched.
func (s *Store) SetReserved(ctx context.Context, used []netip.Prefix) (int, error) {
	if _, err := s.pool.Exec(ctx, `UPDATE transit_blocks b SET reserved = EXISTS (SELECT 1 FROM unnest($1::cidr[]) u WHERE u && b.cidr)
		WHERE peering_id IS NULL`, prefixStrings(used)); err != nil {
		return 0, err
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM transit_blocks WHERE reserved AND peering_id IS NULL`).Scan(&n)
	return n, err
}
