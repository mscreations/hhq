// Copyright (C) 2026 Jon Shaulis
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package models

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ChoreStatus string

const (
	StatusIncomplete      ChoreStatus = "incomplete"
	StatusPendingApproval ChoreStatus = "pending_approval"
	StatusApproved        ChoreStatus = "approved"
	StatusRejected        ChoreStatus = "rejected"
)

// Weekday bitmask helpers. Bit position matches Go's time.Weekday (Sunday = 0).
func WeekdayBit(d time.Weekday) int { return 1 << uint(d) }

func DaysOfWeekContains(mask int, d time.Weekday) bool {
	return mask&WeekdayBit(d) != 0
}

// Chore is the shared catalog entry a parent defines once - name plus the
// description of what's expected for completion - independent of which
// child(ren) it's assigned to (see ChoreDefinition).
type Chore struct {
	ID          int
	Name        string
	Description sql.NullString
	Active      bool
	CreatedAt   time.Time
	// True if this catalog entry is created/kept in sync by the chores.json
	// bootstrap config file (see internal/handlers/bootstrap_config.go)
	// rather than through the parent dashboard. Read-only in the UI for the
	// same reason bootstrap-managed calendar accounts are.
	BootstrapManaged bool
}

type ChoreStore struct {
	DB *sql.DB
}

func (s *ChoreStore) ListActive(ctx context.Context) ([]Chore, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, description, active, created_at, bootstrap_managed
		FROM hhq_chores
		WHERE active = TRUE
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chore
	for rows.Next() {
		var c Chore
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Active, &c.CreatedAt, &c.BootstrapManaged); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *ChoreStore) GetByID(ctx context.Context, id int) (*Chore, error) {
	var c Chore
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, name, description, active, created_at, bootstrap_managed FROM hhq_chores WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.Description, &c.Active, &c.CreatedAt, &c.BootstrapManaged)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetByName looks up an active catalog chore by name, used by the
// chores.json bootstrap reconciliation (see
// internal/handlers/bootstrap_config.go) to decide whether a config entry
// needs to create a new row or update an existing one. Returns ErrNotFound
// if no active chore has that name.
func (s *ChoreStore) GetByName(ctx context.Context, name string) (*Chore, error) {
	var c Chore
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, name, description, active, created_at, bootstrap_managed FROM hhq_chores WHERE active = TRUE AND name = $1`, name).
		Scan(&c.ID, &c.Name, &c.Description, &c.Active, &c.CreatedAt, &c.BootstrapManaged)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *ChoreStore) Create(ctx context.Context, name, description string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_chores (name, description) VALUES ($1, $2) RETURNING id`,
		name, nullableString(description)).Scan(&id)
	return id, err
}

// CreateBootstrap creates a catalog chore marked bootstrap_managed, used by
// the chores.json bootstrap reconciliation.
func (s *ChoreStore) CreateBootstrap(ctx context.Context, name, description string) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_chores (name, description, bootstrap_managed) VALUES ($1, $2, TRUE) RETURNING id`,
		name, nullableString(description)).Scan(&id)
	return id, err
}

// UpdateBootstrap refreshes a bootstrap-managed chore's description to match
// chores.json on every startup (name is the reconciliation key, so it never
// changes here).
func (s *ChoreStore) UpdateBootstrap(ctx context.Context, id int, description string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_chores SET description = $2 WHERE id = $1`,
		id, nullableString(description))
	return err
}

// Update renames/redescribes a catalog chore. Since every assignment joins
// against this row (see ChoreDefinitionStore.ListActive etc.), the change is
// immediately visible everywhere the chore is assigned - there's no per-child
// copy to keep in sync.
func (s *ChoreStore) Update(ctx context.Context, id int, name, description string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_chores SET name = $2, description = $3 WHERE id = $1`,
		id, name, nullableString(description))
	return err
}

// Deactivate soft-deletes a catalog chore (kept for the weekly report's
// history) and cascades the same soft-delete/cleanup that
// ChoreDefinitionStore.Deactivate applies to every assignment of it, so
// removing a chore from the catalog also removes it from every child it was
// assigned to rather than leaving orphaned-looking assignments behind.
func (s *ChoreStore) Deactivate(ctx context.Context, id int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE hhq_chores SET active = FALSE WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhq_chore_definitions SET active = FALSE WHERE chore_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hhq_chore_instances
		WHERE chore_definition_id IN (SELECT id FROM hhq_chore_definitions WHERE chore_id = $1)
		  AND status IN ('incomplete', 'pending_approval', 'rejected')`, id); err != nil {
		return err
	}
	return tx.Commit()
}

type ChoreDefinition struct {
	ID        int
	ChildID   int
	ChildName string // joined from users
	// AssigneeRole is the joined role of the user this chore is assigned to.
	// Assigning a chore to a parent (RoleParent) instead of a child makes it
	// purely informational: no points, never flagged late, and excluded from
	// the weekly report - see ChoreInstanceStore.MarkComplete/ListForWeek.
	AssigneeRole UserRole
	ChoreID      int
	Name         string         // joined from chores
	Description  sql.NullString // joined from chores
	Points       int
	DaysOfWeek   sql.NullInt32 // bitmask; NULL for one-off
	OneOffDate   sql.NullTime
	Active       bool
	CreatedAt    time.Time
	// True if this assignment is created/kept in sync by the
	// assignments.json bootstrap config file (see
	// internal/handlers/bootstrap_config.go) rather than through the parent
	// dashboard. Read-only in the UI for the same reason bootstrap-managed
	// calendar accounts are.
	BootstrapManaged bool
}

type ChoreDefinitionStore struct {
	DB *sql.DB
}

// DaysOfWeekLabel renders the recurring days as e.g. "Sun, Tue, Thu" for
// display on the parent dashboard. Returns "" if not a recurring chore.
func (c ChoreDefinition) DaysOfWeekLabel() string {
	if !c.DaysOfWeek.Valid {
		return ""
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	mask := int(c.DaysOfWeek.Int32)
	var days []string
	for d := time.Sunday; d <= time.Saturday; d++ {
		if DaysOfWeekContains(mask, d) {
			days = append(days, names[d])
		}
	}
	return strings.Join(days, ", ")
}

func (s *ChoreDefinitionStore) ListActive(ctx context.Context) ([]ChoreDefinition, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT cd.id, cd.child_id, u.name, u.role, cd.chore_id, c.name, c.description, cd.points, cd.days_of_week, cd.one_off_date, cd.active, cd.created_at, cd.bootstrap_managed
		FROM hhq_chore_definitions cd
		JOIN hhq_users u ON u.id = cd.child_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		WHERE cd.active = TRUE
		ORDER BY u.name, c.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChoreDefinitions(rows)
}

// GetByChildAndChore looks up the active assignment of a catalog chore to a
// child, used by the assignments.json bootstrap reconciliation (see
// internal/handlers/bootstrap_config.go) to decide whether a config entry
// needs to create a new assignment or update an existing one. Returns
// ErrNotFound if no active assignment exists for that pair.
func (s *ChoreDefinitionStore) GetByChildAndChore(ctx context.Context, childID, choreID int) (*ChoreDefinition, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT cd.id, cd.child_id, u.name, u.role, cd.chore_id, c.name, c.description, cd.points, cd.days_of_week, cd.one_off_date, cd.active, cd.created_at, cd.bootstrap_managed
		FROM hhq_chore_definitions cd
		JOIN hhq_users u ON u.id = cd.child_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		WHERE cd.active = TRUE AND cd.child_id = $1 AND cd.chore_id = $2`, childID, choreID)
	c, err := scanChoreDefinition(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *ChoreDefinitionStore) CreateRecurring(ctx context.Context, childID, choreID, points, daysOfWeek int) (int, error) {
	return s.createRecurring(ctx, childID, choreID, points, daysOfWeek, false)
}

// CreateRecurringBootstrap is CreateRecurring, but marks the assignment
// bootstrap_managed - used by the assignments.json bootstrap reconciliation.
func (s *ChoreDefinitionStore) CreateRecurringBootstrap(ctx context.Context, childID, choreID, points, daysOfWeek int) (int, error) {
	return s.createRecurring(ctx, childID, choreID, points, daysOfWeek, true)
}

func (s *ChoreDefinitionStore) createRecurring(ctx context.Context, childID, choreID, points, daysOfWeek int, bootstrapManaged bool) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_chore_definitions (child_id, chore_id, points, days_of_week, bootstrap_managed)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		childID, choreID, points, daysOfWeek, bootstrapManaged).Scan(&id)
	return id, err
}

func (s *ChoreDefinitionStore) CreateOneOff(ctx context.Context, childID, choreID, points int, date time.Time) (int, error) {
	return s.createOneOff(ctx, childID, choreID, points, date, false)
}

// CreateOneOffBootstrap is CreateOneOff, but marks the assignment
// bootstrap_managed - used by the assignments.json bootstrap reconciliation.
func (s *ChoreDefinitionStore) CreateOneOffBootstrap(ctx context.Context, childID, choreID, points int, date time.Time) (int, error) {
	return s.createOneOff(ctx, childID, choreID, points, date, true)
}

func (s *ChoreDefinitionStore) createOneOff(ctx context.Context, childID, choreID, points int, date time.Time, bootstrapManaged bool) (int, error) {
	var id int
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO hhq_chore_definitions (child_id, chore_id, points, one_off_date, bootstrap_managed)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		childID, choreID, points, date, bootstrapManaged).Scan(&id)
	return id, err
}

// UpdateBootstrapRecurring refreshes a bootstrap-managed recurring
// assignment's points/schedule to match assignments.json on every startup.
// If the assignment was previously one-off, this also clears one_off_date so
// the recurring_xor_one_off CHECK constraint is satisfied.
func (s *ChoreDefinitionStore) UpdateBootstrapRecurring(ctx context.Context, id, points, daysOfWeek int) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_chore_definitions SET points = $2, days_of_week = $3, one_off_date = NULL WHERE id = $1`,
		id, points, daysOfWeek)
	return err
}

// UpdateBootstrapOneOff refreshes a bootstrap-managed one-off assignment's
// points/date to match assignments.json on every startup. If the assignment
// was previously recurring, this also clears days_of_week so the
// recurring_xor_one_off CHECK constraint is satisfied.
func (s *ChoreDefinitionStore) UpdateBootstrapOneOff(ctx context.Context, id, points int, date time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_chore_definitions SET points = $2, one_off_date = $3, days_of_week = NULL WHERE id = $1`,
		id, points, date)
	return err
}

// Deactivate soft-deletes a chore definition (kept for the weekly report's
// history) and removes any of its instances that aren't approved, so a
// deleted chore stops showing up on the kiosk immediately rather than
// lingering until it's individually resolved.
func (s *ChoreDefinitionStore) Deactivate(ctx context.Context, id int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE hhq_chore_definitions SET active = FALSE WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hhq_chore_instances
		WHERE chore_definition_id = $1 AND status IN ('incomplete', 'pending_approval', 'rejected')`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func scanChoreDefinitions(rows *sql.Rows) ([]ChoreDefinition, error) {
	var out []ChoreDefinition
	for rows.Next() {
		var c ChoreDefinition
		if err := rows.Scan(&c.ID, &c.ChildID, &c.ChildName, &c.AssigneeRole, &c.ChoreID, &c.Name, &c.Description, &c.Points, &c.DaysOfWeek, &c.OneOffDate, &c.Active, &c.CreatedAt, &c.BootstrapManaged); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanChoreDefinition(row rowScanner) (*ChoreDefinition, error) {
	var c ChoreDefinition
	if err := row.Scan(&c.ID, &c.ChildID, &c.ChildName, &c.AssigneeRole, &c.ChoreID, &c.Name, &c.Description, &c.Points, &c.DaysOfWeek, &c.OneOffDate, &c.Active, &c.CreatedAt, &c.BootstrapManaged); err != nil {
		return nil, err
	}
	return &c, nil
}

func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// --- Chore instances ---

type ChoreInstance struct {
	ID                int
	ChoreDefinitionID int
	ChildID           int
	ChildName         string // joined
	ChildColor        string // joined
	// AssigneeRole is the joined role of the assigned user. See
	// ChoreDefinition.AssigneeRole for what a RoleParent assignment means.
	AssigneeRole UserRole
	ChoreName    string         // joined
	Description  sql.NullString // joined, chore definition's expectations text
	Points       int            // joined
	DueDate      time.Time
	Status       ChoreStatus
	CompletedAt  sql.NullTime
	DecidedAt    sql.NullTime
	DecidedBy    sql.NullInt32
	CreatedAt    time.Time
	IsLate       bool // computed at read time, not stored - see ListActiveForDate
}

type ChoreInstanceStore struct {
	DB *sql.DB
}

// EnsureForDate creates a chore_instance for every active definition that applies
// to the given date, if one doesn't already exist. Safe to call repeatedly
// (e.g. by the daily scheduler, or lazily on each kiosk page load) thanks to the
// unique (chore_definition_id, due_date) constraint + ON CONFLICT DO NOTHING.
func (s *ChoreInstanceStore) EnsureForDate(ctx context.Context, date time.Time) error {
	weekday := date.Weekday()
	dateOnly := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())

	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO hhq_chore_instances (chore_definition_id, child_id, due_date)
		SELECT id, child_id, $1::date
		FROM hhq_chore_definitions
		WHERE active = TRUE
		  AND (
		        (days_of_week IS NOT NULL AND (days_of_week & $2) <> 0)
		     OR (one_off_date IS NOT NULL AND one_off_date = $1::date)
		      )
		ON CONFLICT (chore_definition_id, due_date) DO NOTHING`,
		dateOnly, WeekdayBit(weekday))
	return err
}

// ListForDate returns all chore instances due on the given date, joined with
// child and chore name/points for display.
func (s *ChoreInstanceStore) ListForDate(ctx context.Context, date time.Time) ([]ChoreInstance, error) {
	dateOnly := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ci.id, ci.chore_definition_id, ci.child_id, u.name, u.color, u.role, c.name, c.description, cd.points,
		       ci.due_date, ci.status, ci.completed_at, ci.decided_at, ci.decided_by, ci.created_at
		FROM hhq_chore_instances ci
		JOIN hhq_chore_definitions cd ON cd.id = ci.chore_definition_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		JOIN hhq_users u ON u.id = ci.child_id
		WHERE ci.due_date = $1::date
		ORDER BY u.name, c.name`, dateOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChoreInstances(rows)
}

// ListForWeek returns all instances with due_date in [weekStart, weekStart+7days),
// used for the weekly report and the dashboard's pending-approvals list.
// Excludes chores assigned to a parent (see ChoreDefinition.AssigneeRole) -
// those are purely informational and never belong in the weekly report.
func (s *ChoreInstanceStore) ListForWeek(ctx context.Context, weekStart time.Time) ([]ChoreInstance, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ci.id, ci.chore_definition_id, ci.child_id, u.name, u.color, u.role, c.name, c.description, cd.points,
		       ci.due_date, ci.status, ci.completed_at, ci.decided_at, ci.decided_by, ci.created_at
		FROM hhq_chore_instances ci
		JOIN hhq_chore_definitions cd ON cd.id = ci.chore_definition_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		JOIN hhq_users u ON u.id = ci.child_id
		WHERE ci.due_date >= $1::date AND ci.due_date < $1::date + interval '7 days'
		  AND u.role = 'child'
		ORDER BY u.name, ci.due_date`, weekStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChoreInstances(rows)
}

// ListActiveForDate returns one row per (chore_definition, child) that should
// still be visible on the kiosk for the given date: either the instance due
// on that date, or - if the chore hasn't been resolved from an earlier day -
// its oldest still-open instance. EnsureForDate creates a fresh instance per
// due date, so a missed recurring chore can end up with several backlog rows
// (one per missed day) plus today's; rather than showing each as a separate
// tile, they're collapsed here into a single entry with IsLate set whenever
// any unresolved backlog exists, so the kiosk shows one LATE-flagged tile
// instead of a cluttered stack of duplicates.
func (s *ChoreInstanceStore) ListActiveForDate(ctx context.Context, date time.Time) ([]ChoreInstance, error) {
	dateOnly := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, date.Location())
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ci.id, ci.chore_definition_id, ci.child_id, COALESCE(u.display_name, u.name), u.color, u.role, c.name, c.description, cd.points,
		       ci.due_date, ci.status, ci.completed_at, ci.decided_at, ci.decided_by, ci.created_at
		FROM hhq_chore_instances ci
		JOIN hhq_chore_definitions cd ON cd.id = ci.chore_definition_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		JOIN hhq_users u ON u.id = ci.child_id
		WHERE ci.due_date = $1::date
		   OR (ci.due_date < $1::date AND ci.status IN ('incomplete', 'pending_approval', 'rejected'))
		ORDER BY u.name, c.name, ci.due_date`, dateOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	instances, err := scanChoreInstances(rows)
	if err != nil {
		return nil, err
	}
	// Postgres DATE columns come back from the driver as UTC-located
	// time.Time values regardless of dateOnly's location, so compare against
	// a UTC-normalized threshold rather than dateOnly directly - otherwise a
	// non-UTC server timezone can make today's own instance compare as
	// "before" today and get wrongly flagged late.
	todayUTC := time.Date(dateOnly.Year(), dateOnly.Month(), dateOnly.Day(), 0, 0, 0, 0, time.UTC)
	return collapseChoreInstances(instances, todayUTC), nil
}

func isUnresolvedChoreStatus(status ChoreStatus) bool {
	return status == StatusIncomplete || status == StatusPendingApproval || status == StatusRejected
}

// collapseChoreInstances groups instances by (chore_definition, child) and
// picks a single representative per group: the instance due "today" if one
// exists (so tapping it acts on the current day's chore), otherwise the most
// recent unresolved backlog instance, otherwise whatever's left (e.g. an
// already-resolved instance with no backlog). IsLate is set on the
// representative whenever the group contains ANY unresolved instance from
// before today, regardless of which instance ends up as the representative.
func collapseChoreInstances(instances []ChoreInstance, todayUTC time.Time) []ChoreInstance {
	type groupKey struct{ choreDefinitionID, childID int }

	var order []groupKey
	groups := map[groupKey][]ChoreInstance{}
	for _, ci := range instances {
		k := groupKey{ci.ChoreDefinitionID, ci.ChildID}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], ci)
	}

	out := make([]ChoreInstance, 0, len(order))
	for _, k := range order {
		group := groups[k]
		var today, latestUnresolved, latestOverall *ChoreInstance
		isLate := false
		for i := range group {
			ci := &group[i]
			// Chores assigned to a parent are purely informational and are
			// never flagged late, regardless of how long they've sat unresolved.
			if ci.AssigneeRole != RoleParent && isUnresolvedChoreStatus(ci.Status) && ci.DueDate.Before(todayUTC) {
				isLate = true
			}
			if ci.DueDate.Equal(todayUTC) {
				today = ci
			}
			if isUnresolvedChoreStatus(ci.Status) && (latestUnresolved == nil || ci.DueDate.After(latestUnresolved.DueDate)) {
				latestUnresolved = ci
			}
			if latestOverall == nil || ci.DueDate.After(latestOverall.DueDate) {
				latestOverall = ci
			}
		}

		primary := today
		if primary == nil {
			primary = latestUnresolved
		}
		if primary == nil {
			primary = latestOverall
		}
		rep := *primary
		rep.IsLate = isLate
		out = append(out, rep)
	}
	return out
}

func (s *ChoreInstanceStore) GetByID(ctx context.Context, id int) (*ChoreInstance, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT ci.id, ci.chore_definition_id, ci.child_id, u.name, u.color, u.role, c.name, c.description, cd.points,
		       ci.due_date, ci.status, ci.completed_at, ci.decided_at, ci.decided_by, ci.created_at
		FROM hhq_chore_instances ci
		JOIN hhq_chore_definitions cd ON cd.id = ci.chore_definition_id
		JOIN hhq_chores c ON c.id = cd.chore_id
		JOIN hhq_users u ON u.id = ci.child_id
		WHERE ci.id = $1`, id)
	return scanChoreInstance(row)
}

// MarkComplete is called when a child or parent taps their chore on the
// kiosk - either the first time (status was 'incomplete') or to resubmit
// after a rejection (status was 'rejected'). For a chore assigned to a
// child, it transitions to 'pending_approval' and the caller is expected to
// send the parent notification (see internal/handlers/kiosk.go). For a
// chore assigned to a parent (informational only, no points), it instead
// transitions straight to 'approved' with no approval step or email at all.
// Returns the resulting status, and ErrInvalidTransition if the chore wasn't
// in either 'incomplete' or 'rejected' (e.g. already pending or approved -
// most commonly a double-tap, which the kiosk JS also debounces client-side).
func (s *ChoreInstanceStore) MarkComplete(ctx context.Context, id int) (ChoreStatus, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var status ChoreStatus
	var choreDefinitionID, childID int
	var dueDate time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE hhq_chore_instances ci
		SET status = (CASE WHEN u.role = 'parent' THEN 'approved' ELSE 'pending_approval' END)::chore_status,
		    completed_at = now(),
		    decided_at = CASE WHEN u.role = 'parent' THEN now() ELSE NULL END,
		    decided_by = NULL
		FROM hhq_users u
		WHERE ci.id = $1 AND ci.child_id = u.id AND ci.status IN ('incomplete', 'rejected')
		RETURNING ci.status, ci.chore_definition_id, ci.child_id, ci.due_date`,
		id).Scan(&status, &choreDefinitionID, &childID, &dueDate)
	if err == sql.ErrNoRows {
		return "", ErrInvalidTransition
	}
	if err != nil {
		return "", err
	}

	if status == StatusApproved {
		// Parent-assigned informational chore: cascade-approve any earlier
		// unresolved backlog too (mirroring Decide's approval cascade for
		// children), since there's no separate approval step to trigger it.
		if _, err := tx.ExecContext(ctx, `
			UPDATE hhq_chore_instances
			SET status = 'approved', completed_at = COALESCE(completed_at, now()), decided_at = now()
			WHERE chore_definition_id = $1 AND child_id = $2 AND due_date < $3
			  AND status IN ('incomplete', 'pending_approval', 'rejected')`,
			choreDefinitionID, childID, dueDate); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}

// Decide is called from the signed email link when a parent approves or rejects.
// parentID may be 0 if the responder's identity isn't known (e.g. they clicked
// a no-login email link), in which case decided_by is left NULL.
//
// On approval, any earlier still-unresolved instance of the same chore
// definition for the same child (i.e. previous days the chore was missed,
// see ListActiveForDate) is approved along with it - doing the chore once
// catches up the backlog rather than requiring the child to redo it once per
// missed day. Rejection does not cascade: only this instance is affected.
func (s *ChoreInstanceStore) Decide(ctx context.Context, id int, approve bool, parentID int) error {
	status := StatusApproved
	if !approve {
		status = StatusRejected
	}
	var decidedBy sql.NullInt32
	if parentID > 0 {
		decidedBy = sql.NullInt32{Int32: int32(parentID), Valid: true}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var choreDefinitionID, childID int
	var dueDate time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE hhq_chore_instances
		SET status = $2, decided_at = now(), decided_by = $3
		WHERE id = $1 AND status = 'pending_approval'
		RETURNING chore_definition_id, child_id, due_date`,
		id, status, decidedBy).Scan(&choreDefinitionID, &childID, &dueDate)
	if err == sql.ErrNoRows {
		return ErrInvalidTransition
	}
	if err != nil {
		return err
	}

	if approve {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hhq_chore_instances
			SET status = 'approved', completed_at = COALESCE(completed_at, now()), decided_at = now(), decided_by = $4
			WHERE chore_definition_id = $1 AND child_id = $2 AND due_date < $3
			  AND status IN ('incomplete', 'pending_approval', 'rejected')`,
			choreDefinitionID, childID, dueDate, decidedBy); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ResetRejected flips a rejected chore back to incomplete so the child can retry,
// per the agreed UX: rejections are handled out-of-band (the parent talks to the
// child directly), and the child simply tries again.
func (s *ChoreInstanceStore) ResetRejected(ctx context.Context, id int) error {
	res, err := s.DB.ExecContext(ctx, `
		UPDATE hhq_chore_instances SET status = 'incomplete', completed_at = NULL, decided_at = NULL, decided_by = NULL
		WHERE id = $1 AND status = 'rejected'`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInvalidTransition
	}
	return nil
}

func scanChoreInstances(rows *sql.Rows) ([]ChoreInstance, error) {
	var out []ChoreInstance
	for rows.Next() {
		var c ChoreInstance
		if err := rows.Scan(&c.ID, &c.ChoreDefinitionID, &c.ChildID, &c.ChildName, &c.ChildColor, &c.AssigneeRole, &c.ChoreName, &c.Description, &c.Points,
			&c.DueDate, &c.Status, &c.CompletedAt, &c.DecidedAt, &c.DecidedBy, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanChoreInstance(row rowScanner) (*ChoreInstance, error) {
	var c ChoreInstance
	if err := row.Scan(&c.ID, &c.ChoreDefinitionID, &c.ChildID, &c.ChildName, &c.ChildColor, &c.AssigneeRole, &c.ChoreName, &c.Description, &c.Points,
		&c.DueDate, &c.Status, &c.CompletedAt, &c.DecidedAt, &c.DecidedBy, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}
