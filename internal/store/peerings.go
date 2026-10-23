package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zufardhiyaulhaq/openstack-router-peering/internal/model"
)

// peeringCols is the SELECT list scanned by scanPeering.
const peeringCols = `id, name, status::text, coalesce(status_reason, ''),
	requester_project_id, requester_router_id::text, accepter_project_id, coalesce(accepter_router_id::text, ''),
	transit_cidr, coalesce(transit_network_id::text, ''), coalesce(transit_subnet_id::text, ''),
	coalesce(requester_port_id::text, ''), coalesce(accepter_port_id::text, ''), requester_ip, accepter_ip,
	attempts, next_attempt_at, coalesce(claimed_by, ''), claimed_until, warnings,
	created_by, coalesce(accepted_by, ''), coalesce(deleted_by, ''), created_at, updated_at, expires_at, deleted_at`

func scanPeering(row pgx.Row) (model.Peering, error) {
	var p model.Peering
	var status string
	var cidr *netip.Prefix
	var rip, aip *netip.Addr
	var claimedUntil, deletedAt *time.Time
	var warnings []byte
	err := row.Scan(&p.ID, &p.Name, &status, &p.StatusReason,
		&p.RequesterProjectID, &p.RequesterRouterID, &p.AccepterProjectID, &p.AccepterRouterID,
		&cidr, &p.TransitNetworkID, &p.TransitSubnetID, &p.RequesterPortID, &p.AccepterPortID, &rip, &aip,
		&p.Attempts, &p.NextAttemptAt, &p.ClaimedBy, &claimedUntil, &warnings,
		&p.CreatedBy, &p.AcceptedBy, &p.DeletedBy, &p.CreatedAt, &p.UpdatedAt, &p.ExpiresAt, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Status = model.Status(status)
	if cidr != nil {
		p.TransitCIDR = *cidr
	}
	if rip != nil {
		p.RequesterIP = *rip
	}
	if aip != nil {
		p.AccepterIP = *aip
	}
	if claimedUntil != nil {
		p.ClaimedUntil = *claimedUntil
	}
	if deletedAt != nil {
		p.DeletedAt = *deletedAt
	}
	if len(warnings) > 0 {
		if err := json.Unmarshal(warnings, &p.Warnings); err != nil {
			return p, err
		}
	}
	return p, nil
}

func scanAll(rows pgx.Rows) ([]model.Peering, error) {
	defer rows.Close()
	var out []model.Peering
	for rows.Next() {
		p, err := scanPeering(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// nullStr maps "" to SQL NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && (constraint == "" || pg.ConstraintName == constraint)
}

func statusTexts(st []model.Status) []string {
	out := make([]string, len(st))
	for i, s := range st {
		out[i] = string(s)
	}
	return out
}

// CreatePeering inserts p as pending_acceptance, sets p.ID and the DB-filled fields, and writes a "requested" event.
func (s *Store) CreatePeering(ctx context.Context, p *model.Peering) error {
	for attempt := 0; attempt < 5; attempt++ {
		p.ID = model.NewID()
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		var expires any
		if !p.ExpiresAt.IsZero() {
			expires = p.ExpiresAt
		}
		row := tx.QueryRow(ctx, `INSERT INTO peerings (id, name, requester_project_id, requester_router_id, accepter_project_id, created_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, coalesce($7, now() + interval '7 days'))
			RETURNING `+peeringCols,
			p.ID, p.Name, p.RequesterProjectID, p.RequesterRouterID, p.AccepterProjectID, p.CreatedBy, expires)
		got, err := scanPeering(row)
		if err != nil {
			_ = tx.Rollback(ctx)
			if isUniqueViolation(err, "peerings_pkey") {
				continue
			}
			return err
		}
		if err := addEvent(ctx, tx, model.Event{PeeringID: got.ID, Action: model.ActionRequested, ActorUserID: p.CreatedBy, ActorProjectID: p.RequesterProjectID}); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		*p = got
		return nil
	}
	return errors.New("could not generate a unique peering id")
}

// GetPeering returns one peering or ErrNotFound.
func (s *Store) GetPeering(ctx context.Context, id string) (model.Peering, error) {
	return scanPeering(s.pool.QueryRow(ctx, "SELECT "+peeringCols+" FROM peerings WHERE id = $1", id))
}

// ListFilter selects peerings for ListPeerings.
type ListFilter struct {
	ProjectID string
	Direction string // "incoming", "outgoing" or "" for both
	Statuses  []model.Status
	All       bool // cloud admin: ignore ProjectID
	Limit     int
	Marker    string
}

// ListPeerings returns peerings ordered by created_at, id.
func (s *Store) ListPeerings(ctx context.Context, f ListFilter) ([]model.Peering, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !f.All {
		p := arg(f.ProjectID)
		switch f.Direction {
		case "outgoing":
			where = append(where, "requester_project_id = "+p)
		case "incoming":
			where = append(where, "accepter_project_id = "+p)
		default:
			where = append(where, "(requester_project_id = "+p+" OR accepter_project_id = "+p+")")
		}
	}
	if len(f.Statuses) > 0 {
		where = append(where, "status::text = ANY("+arg(statusTexts(f.Statuses))+"::text[])")
	}
	if f.Marker != "" {
		m := arg(f.Marker)
		where = append(where, "(created_at, id) > (SELECT created_at, id FROM peerings WHERE id = "+m+")")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	q := "SELECT " + peeringCols + " FROM peerings"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at, id LIMIT " + arg(limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

// ListByStatus returns every peering in one of the given statuses.
func (s *Store) ListByStatus(ctx context.Context, st ...model.Status) ([]model.Peering, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+peeringCols+" FROM peerings WHERE status::text = ANY($1::text[]) ORDER BY created_at, id", statusTexts(st))
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

// CountNonTerminal counts the project's peerings, either side, that are not in a terminal state.
func (s *Store) CountNonTerminal(ctx context.Context, projectID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM peerings
		WHERE (requester_project_id = $1 OR accepter_project_id = $1) AND NOT (status::text = ANY($2::text[]))`,
		projectID, statusTexts(model.TerminalStatuses)).Scan(&n)
	return n, err
}

// Patch lists the columns Transition may set; nil means unchanged.
type Patch struct {
	StatusReason, AccepterRouterID, AcceptedBy, DeletedBy *string
	Warnings                                              *[]string
	ResetAttempts                                         bool
}

// Transition moves a peering from one of `from` to `to`, applies patch and records ev, in one transaction.
func (s *Store) Transition(ctx context.Context, id string, from []model.Status, to model.Status, patch Patch, ev model.Event) (model.Peering, error) {
	sets := []string{"status = $2::text::peering_status", "updated_at = now()"}
	args := []any{id, string(to), statusTexts(from)}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if patch.StatusReason != nil {
		set("status_reason", nullStr(*patch.StatusReason))
	}
	if patch.AccepterRouterID != nil {
		set("accepter_router_id", nullStr(*patch.AccepterRouterID))
	}
	if patch.AcceptedBy != nil {
		set("accepted_by", nullStr(*patch.AcceptedBy))
	}
	if patch.DeletedBy != nil {
		set("deleted_by", nullStr(*patch.DeletedBy))
	}
	if patch.Warnings != nil {
		w := *patch.Warnings
		if w == nil {
			w = []string{}
		}
		b, _ := json.Marshal(w)
		set("warnings", string(b))
	}
	if patch.ResetAttempts {
		sets = append(sets, "attempts = 0")
	}
	if to == model.StatusProvisioning || to == model.StatusDeleting {
		sets = append(sets, "claimed_by = NULL", "claimed_until = NULL", "next_attempt_at = now()")
	}
	if to == model.StatusDeleted {
		sets = append(sets, "deleted_at = now()")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Peering{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	row := tx.QueryRow(ctx, "UPDATE peerings SET "+strings.Join(sets, ", ")+
		" WHERE id = $1 AND status::text = ANY($3::text[]) RETURNING "+peeringCols, args...)
	p, err := scanPeering(row)
	if isUniqueViolation(err, "peerings_router_pair") {
		return p, fmt.Errorf("%w: these routers are already peered", ErrConflict)
	}
	if errors.Is(err, ErrNotFound) {
		if _, gerr := s.GetPeering(ctx, id); errors.Is(gerr, ErrNotFound) {
			return p, ErrNotFound
		}
		return p, ErrConflict
	}
	if err != nil {
		return p, err
	}
	ev.PeeringID = id
	if err := addEvent(ctx, tx, ev); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func addEvent(ctx context.Context, db execer, e model.Event) error {
	if e.Action == "" {
		return errors.New("event action is required")
	}
	var detail any
	if e.Detail != nil {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	_, err := db.Exec(ctx, `INSERT INTO peering_events (peering_id, actor_user_id, actor_project_id, action, detail)
		VALUES ($1, $2, $3, $4, $5::jsonb)`, e.PeeringID, nullStr(e.ActorUserID), nullStr(e.ActorProjectID), e.Action, detail)
	return err
}

// AddEvent appends one audit event.
func (s *Store) AddEvent(ctx context.Context, e model.Event) error { return addEvent(ctx, s.pool, e) }

// ListEvents returns a peering's events, oldest first.
func (s *Store) ListEvents(ctx context.Context, peeringID string) ([]model.Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, peering_id, at, coalesce(actor_user_id, ''), coalesce(actor_project_id, ''), action, detail
		FROM peering_events WHERE peering_id = $1 ORDER BY id`, peeringID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var e model.Event
		var detail []byte
		if err := rows.Scan(&e.ID, &e.PeeringID, &e.At, &e.ActorUserID, &e.ActorProjectID, &e.Action, &detail); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &e.Detail); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastAction returns the newest event action not in skip, or "" if none.
func (s *Store) LastAction(ctx context.Context, peeringID string, skip ...string) (string, error) {
	if skip == nil {
		skip = []string{}
	}
	var a string
	err := s.pool.QueryRow(ctx, `SELECT action FROM peering_events WHERE peering_id = $1 AND NOT (action = ANY($2::text[]))
		ORDER BY id DESC LIMIT 1`, peeringID, skip).Scan(&a)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return a, err
}

// SetWarnings replaces a peering's warnings without changing its status.
func (s *Store) SetWarnings(ctx context.Context, id string, warnings []string) error {
	if warnings == nil {
		warnings = []string{}
	}
	b, _ := json.Marshal(warnings)
	_, err := s.pool.Exec(ctx, `UPDATE peerings SET warnings = $2::jsonb, updated_at = now() WHERE id = $1`, id, string(b))
	return err
}
