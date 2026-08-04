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

package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/mscreations/hhq/internal/auth"
	"github.com/mscreations/hhq/internal/config"
	"github.com/mscreations/hhq/internal/logging"
	"github.com/mscreations/hhq/internal/models"
)

// BootstrapParents reconciles the parents.json bootstrap file against the
// database on every startup, replacing the old one-shot
// BOOTSTRAP_PARENT_NAME/_EMAIL/_PASSWORD/_AVATAR_FILE env vars. Follows the
// same by-email reconciliation pattern as BootstrapChildren: parents not yet
// present (matched by email) are created, and parents already
// present that were themselves created by bootstrap have their password,
// color, and display name refreshed to match every time - the config file is
// the standing source of truth for them, which is also why they're marked
// BootstrapManaged and refused edits/removal through the parent UI (see
// SetParentDisplayName/RemoveUser). A collision with a parent created
// through the dashboard (invite flow) is skipped (logged, not overwritten).
// Any existing BootstrapManaged parent whose email no longer appears in
// entries is deactivated - unless doing so would leave zero active parents,
// which is refused (logged) instead, mirroring RemoveUser's "can't remove
// the last remaining parent" guard, since there would otherwise be no way
// to recover login access.
func (a *App) BootstrapParents(ctx context.Context, entries []config.ParentBootstrap) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Name == "" {
			logging.Errorf("bootstrap: skipping parents.json entry: name is required")
			continue
		}
		email, err := e.ResolveEmail()
		if err != nil {
			logging.Errorf("bootstrap: skipping parents.json entry %q: %v", e.Name, err)
			continue
		}
		if email == "" {
			logging.Errorf("bootstrap: skipping parents.json entry %q: email is required", e.Name)
			continue
		}
		password, err := e.ResolvePassword()
		if err != nil {
			logging.Errorf("bootstrap: skipping parents.json entry %q: %v", e.Name, err)
			continue
		}
		if password == "" {
			logging.Errorf("bootstrap: skipping parents.json entry %q: password is required", e.Name)
			continue
		}
		seen[email] = true

		hash, err := auth.HashPassword(password)
		if err != nil {
			logging.Errorf("bootstrap: hashing password for parent %q: %v", e.Name, err)
			continue
		}

		var parentID int
		existing, err := a.Users.GetParentByEmail(ctx, email)
		switch {
		case errors.Is(err, models.ErrNotFound):
			color := e.Color
			if color == "" {
				color, err = a.Users.NextAvailableColor(ctx)
				if err != nil {
					logging.Errorf("bootstrap: picking color for parent %q: %v", e.Name, err)
					continue
				}
			} else if resolved, ok := models.ResolveColor(color); ok {
				color = resolved
			}
			id, err := a.Users.CreateParentBootstrap(ctx, e.Name, email, hash, color, e.DisplayName)
			if err != nil {
				logging.Errorf("bootstrap: creating parent %q: %v", e.Name, err)
				continue
			}
			logging.Infof("bootstrap: created parent %q (id=%d)", e.Name, id)
			parentID = id
		case err != nil:
			logging.Errorf("bootstrap: looking up parent %q: %v", e.Name, err)
			continue
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping %q - a parent with this email already exists and was created via the dashboard, not bootstrap", e.Name)
			continue
		default:
			color := e.Color
			if color == "" {
				color = existing.Color // config left color unset: keep whatever it currently is
			} else if resolved, ok := models.ResolveColor(color); ok {
				color = resolved
			}
			displayName := e.DisplayName
			if displayName == "" {
				displayName = existing.DisplayName.String // config left display_name unset: keep whatever it currently is
			}
			if err := a.Users.UpdateParentBootstrap(ctx, existing.ID, hash, color, displayName); err != nil {
				logging.Errorf("bootstrap: updating parent %q (id=%d): %v", e.Name, existing.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed parent %q (id=%d)", e.Name, existing.ID)
			parentID = existing.ID
		}

		if e.AvatarFile != "" {
			if err := a.applyUserAvatarFile(ctx, parentID, e.AvatarFile); err != nil {
				logging.Errorf("bootstrap: applying avatar_file for parent %q (id=%d): %v", e.Name, parentID, err)
			}
		}
	}

	parents, err := a.Users.ListParents(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing parents for removal reconciliation: %v", err)
		return
	}
	active := len(parents)
	for _, parent := range parents {
		if !parent.BootstrapManaged || seen[parent.Email.String] {
			continue
		}
		if active <= 1 {
			logging.Warnf("bootstrap: refusing to remove last remaining parent %q (id=%d) - no longer in parents.json, but removing them would lock everyone out", parent.Name, parent.ID)
			continue
		}
		if err := a.Users.Deactivate(ctx, parent.ID); err != nil {
			logging.Errorf("bootstrap: removing parent %q (id=%d) no longer in parents.json: %v", parent.Name, parent.ID, err)
			continue
		}
		active--
		logging.Infof("bootstrap: removed parent %q (id=%d) - no longer in parents.json", parent.Name, parent.ID)
	}
}

// BootstrapChildren reconciles the children.json bootstrap file against the
// database on every startup, mirroring BootstrapCalendarAccounts's shape
// (see internal/handlers/sync.go): children not yet present (matched by
// name) are created, and children already present that were themselves
// created by bootstrap have their color refreshed to match every time - the
// config file is the source of truth for them, which is also why they're
// marked BootstrapManaged and refused edits/removal through the parent UI. A
// name collision with a child created through the dashboard is skipped
// (logged, not overwritten) since that child's config didn't come from this
// file. Any existing BootstrapManaged child whose name no longer appears in
// entries is deactivated - the same soft-delete DeactivateChild uses on the
// dashboard, preserving their chore/points history - since otherwise there
// would be no way to remove a child added via bootstrap short of reaching
// into the database directly.
func (a *App) BootstrapChildren(ctx context.Context, entries []config.ChildBootstrap) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.Name] = true
		if e.Name == "" {
			logging.Errorf("bootstrap: skipping children.json entry: name is required")
			continue
		}

		var childID int
		existing, err := a.Users.GetChildByName(ctx, e.Name)
		switch {
		case errors.Is(err, models.ErrNotFound):
			color := e.Color
			if color == "" {
				color, err = a.Users.NextAvailableColor(ctx)
				if err != nil {
					logging.Errorf("bootstrap: picking color for child %q: %v", e.Name, err)
					continue
				}
			} else if resolved, ok := models.ResolveColor(color); ok {
				color = resolved
			}
			id, err := a.Users.CreateChildBootstrap(ctx, e.Name, color)
			if err != nil {
				logging.Errorf("bootstrap: creating child %q: %v", e.Name, err)
				continue
			}
			logging.Infof("bootstrap: created child %q (id=%d)", e.Name, id)
			childID = id
		case err != nil:
			logging.Errorf("bootstrap: looking up child %q: %v", e.Name, err)
			continue
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping %q - a child with this name already exists and was created via the dashboard, not bootstrap", e.Name)
			continue
		default:
			color := e.Color
			if color == "" {
				color = existing.Color // config left color unset: keep whatever it currently is
			} else if resolved, ok := models.ResolveColor(color); ok {
				color = resolved
			}
			if err := a.Users.UpdateChildBootstrap(ctx, existing.ID, color); err != nil {
				logging.Errorf("bootstrap: updating child %q (id=%d): %v", e.Name, existing.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed child %q (id=%d)", e.Name, existing.ID)
			childID = existing.ID
		}

		if e.AvatarFile != "" {
			if err := a.applyUserAvatarFile(ctx, childID, e.AvatarFile); err != nil {
				logging.Errorf("bootstrap: applying avatar_file for child %q (id=%d): %v", e.Name, childID, err)
			}
		}
	}

	children, err := a.Users.ListChildren(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing children for removal reconciliation: %v", err)
		return
	}
	for _, child := range children {
		if !child.BootstrapManaged || seen[child.Name] {
			continue
		}
		if err := a.Users.Deactivate(ctx, child.ID); err != nil {
			logging.Errorf("bootstrap: removing child %q (id=%d) no longer in children.json: %v", child.Name, child.ID, err)
			continue
		}
		logging.Infof("bootstrap: removed child %q (id=%d) - no longer in children.json", child.Name, child.ID)
	}
}

// BootstrapChores reconciles the chores.json bootstrap file against the
// database on every startup, using the same by-name reconciliation pattern
// as BootstrapChildren/BootstrapCalendarAccounts, including deactivating
// (see ChoreStore.Deactivate, same as the dashboard's "Delete" button)
// BootstrapManaged catalog entries no longer present in entries.
func (a *App) BootstrapChores(ctx context.Context, entries []config.ChoreBootstrap) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		seen[e.Name] = true
		if e.Name == "" {
			logging.Errorf("bootstrap: skipping chores.json entry: name is required")
			continue
		}

		existing, err := a.Chores.GetByName(ctx, e.Name)
		switch {
		case errors.Is(err, models.ErrNotFound):
			id, err := a.Chores.CreateBootstrap(ctx, e.Name, e.Description)
			if err != nil {
				logging.Errorf("bootstrap: creating chore %q: %v", e.Name, err)
				continue
			}
			logging.Infof("bootstrap: created chore %q (id=%d)", e.Name, id)
		case err != nil:
			logging.Errorf("bootstrap: looking up chore %q: %v", e.Name, err)
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping %q - a chore with this name already exists and was created via the dashboard, not bootstrap", e.Name)
		default:
			if err := a.Chores.UpdateBootstrap(ctx, existing.ID, e.Description); err != nil {
				logging.Errorf("bootstrap: updating chore %q (id=%d): %v", e.Name, existing.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed chore %q (id=%d)", e.Name, existing.ID)
		}
	}

	chores, err := a.Chores.ListActive(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing chores for removal reconciliation: %v", err)
		return
	}
	for _, chore := range chores {
		if !chore.BootstrapManaged || seen[chore.Name] {
			continue
		}
		if err := a.Chores.Deactivate(ctx, chore.ID); err != nil {
			logging.Errorf("bootstrap: removing chore %q (id=%d) no longer in chores.json: %v", chore.Name, chore.ID, err)
			continue
		}
		logging.Infof("bootstrap: removed chore %q (id=%d) - no longer in chores.json", chore.Name, chore.ID)
	}
}

// BootstrapAssignments reconciles the assignments.json bootstrap file
// against the database on every startup: each entry maps a child (by name,
// resolved via a.Users.GetChildByName) to a catalog chore (by name, resolved
// via a.Chores.GetByName) on a schedule. The referenced child/chore don't
// need to themselves be bootstrap-managed - an assignment can point at a
// child or chore created through the dashboard - only the assignment record
// itself is reconciled/locked here. Any existing BootstrapManaged assignment
// whose (child, chore) pair no longer appears in entries is deactivated (see
// ChoreDefinitionStore.Deactivate, same as the dashboard's "Delete" button),
// since otherwise there would be no way to remove an assignment added via
// bootstrap short of reaching into the database directly.
func (a *App) BootstrapAssignments(ctx context.Context, entries []config.AssignmentBootstrap) {
	type childChore struct{ childID, choreID int }
	seen := make(map[childChore]bool, len(entries))
	for _, e := range entries {
		if e.Child == "" || e.Chore == "" {
			logging.Errorf("bootstrap: skipping assignments.json entry: child and chore are both required")
			continue
		}

		child, err := a.Users.GetChildByName(ctx, e.Child)
		if errors.Is(err, models.ErrNotFound) {
			logging.Errorf("bootstrap: skipping assignment of %q to %q: no such child (add it to children.json first)", e.Chore, e.Child)
			continue
		}
		if err != nil {
			logging.Errorf("bootstrap: looking up child %q: %v", e.Child, err)
			continue
		}

		chore, err := a.Chores.GetByName(ctx, e.Chore)
		if errors.Is(err, models.ErrNotFound) {
			logging.Errorf("bootstrap: skipping assignment of %q to %q: no such chore (add it to chores.json first)", e.Chore, e.Child)
			continue
		}
		if err != nil {
			logging.Errorf("bootstrap: looking up chore %q: %v", e.Chore, err)
			continue
		}

		seen[childChore{child.ID, chore.ID}] = true

		points := e.Points
		if points <= 0 {
			points = 1
		}

		recurring := len(e.DaysOfWeek) > 0
		var daysMask int
		var oneOffDate time.Time
		switch {
		case recurring && e.OneOffDate != "":
			logging.Errorf("bootstrap: skipping assignment of %q to %q: days_of_week and one_off_date are mutually exclusive", e.Chore, e.Child)
			continue
		case recurring:
			invalid := false
			for _, d := range e.DaysOfWeek {
				wd, err := config.ResolveWeekday(d)
				if err != nil {
					logging.Errorf("bootstrap: skipping assignment of %q to %q: %v", e.Chore, e.Child, err)
					invalid = true
					break
				}
				daysMask |= models.WeekdayBit(wd)
			}
			if invalid {
				continue
			}
		case e.OneOffDate != "":
			oneOffDate, err = time.Parse("2006-01-02", e.OneOffDate)
			if err != nil {
				logging.Errorf("bootstrap: skipping assignment of %q to %q: invalid one_off_date %q: %v", e.Chore, e.Child, e.OneOffDate, err)
				continue
			}
		default:
			logging.Errorf("bootstrap: skipping assignment of %q to %q: one of days_of_week or one_off_date is required", e.Chore, e.Child)
			continue
		}

		existing, err := a.ChoreDefs.GetByChildAndChore(ctx, child.ID, chore.ID)
		switch {
		case errors.Is(err, models.ErrNotFound):
			var id int
			if recurring {
				id, err = a.ChoreDefs.CreateRecurringBootstrap(ctx, child.ID, chore.ID, points, daysMask)
			} else {
				id, err = a.ChoreDefs.CreateOneOffBootstrap(ctx, child.ID, chore.ID, points, oneOffDate)
			}
			if err != nil {
				logging.Errorf("bootstrap: creating assignment of %q to %q: %v", chore.Name, child.Name, err)
				continue
			}
			logging.Infof("bootstrap: assigned %q to %q (id=%d)", chore.Name, child.Name, id)
		case err != nil:
			logging.Errorf("bootstrap: looking up assignment of %q to %q: %v", chore.Name, child.Name, err)
		case !existing.BootstrapManaged:
			logging.Warnf("bootstrap: skipping assignment of %q to %q - it already exists and was created via the dashboard, not bootstrap", chore.Name, child.Name)
		default:
			if recurring {
				err = a.ChoreDefs.UpdateBootstrapRecurring(ctx, existing.ID, points, daysMask)
			} else {
				err = a.ChoreDefs.UpdateBootstrapOneOff(ctx, existing.ID, points, oneOffDate)
			}
			if err != nil {
				logging.Errorf("bootstrap: updating assignment of %q to %q (id=%d): %v", chore.Name, child.Name, existing.ID, err)
				continue
			}
			logging.Debugf("bootstrap: refreshed assignment of %q to %q (id=%d)", chore.Name, child.Name, existing.ID)
		}
	}

	defs, err := a.ChoreDefs.ListActive(ctx)
	if err != nil {
		logging.Errorf("bootstrap: listing assignments for removal reconciliation: %v", err)
		return
	}
	for _, def := range defs {
		if !def.BootstrapManaged || seen[childChore{def.ChildID, def.ChoreID}] {
			continue
		}
		if err := a.ChoreDefs.Deactivate(ctx, def.ID); err != nil {
			logging.Errorf("bootstrap: removing assignment of %q to %q (id=%d) no longer in assignments.json: %v", def.Name, def.ChildName, def.ID, err)
			continue
		}
		logging.Infof("bootstrap: removed assignment of %q to %q (id=%d) - no longer in assignments.json", def.Name, def.ChildName, def.ID)
	}
}
