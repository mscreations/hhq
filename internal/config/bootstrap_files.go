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

package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ChildBootstrap is one entry in the CONFIG_DIR/children.json bootstrap
// file - see internal/handlers/bootstrap_config.go's BootstrapChildren for
// how these are reconciled against the database on every startup.
type ChildBootstrap struct {
	Name string `json:"name"`
	// Color is optional - if blank, BootstrapChildren auto-assigns one from
	// the same palette used when a parent adds a child via the dashboard.
	Color string `json:"color"`
	// AvatarFile is optional - a path to an image file, resolved relative to
	// CONFIG_DIR unless absolute (see config.ConfigDir), applied as this
	// child's avatar on every startup. Blank means "don't touch this child's
	// avatar" (same convention as a blank Color). For a bootstrap-managed
	// child, this always wins over any avatar uploaded via the dashboard in
	// the meantime - see BootstrapChildren.
	AvatarFile string `json:"avatar_file"`
}

// ParseChildrenBootstrap unmarshals CONFIG_DIR/children.json's contents - a
// JSON array of ChildBootstrap entries. Per-entry validation (missing name)
// happens later in BootstrapChildren, not here.
func ParseChildrenBootstrap(raw string) ([]ChildBootstrap, error) {
	var entries []ChildBootstrap
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing children.json: %w", err)
	}
	return entries, nil
}

// ChoreBootstrap is one entry in the CONFIG_DIR/chores.json bootstrap file -
// the shared catalog entry (name + description), independent of which
// child(ren) it's assigned to (see AssignmentBootstrap). See
// internal/handlers/bootstrap_config.go's BootstrapChores for how these are
// reconciled against the database on every startup.
type ChoreBootstrap struct {
	Name string `json:"name"`
	// Description accepts either a plain JSON string, or a JSON array of
	// strings that gets joined with "\n" - the array form avoids hand-typing
	// "\n" escapes for multi-line instructions. See
	// ChoreBootstrap.UnmarshalJSON.
	Description string `json:"description"`
}

// UnmarshalJSON lets ChoreBootstrap.Description be written in chores.json as
// either a plain string or a JSON array of strings (joined with "\n"),
// rather than forcing every multi-line description to be hand-escaped as
// "line one\nline two".
func (c *ChoreBootstrap) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string          `json:"name"`
		Description json.RawMessage `json:"description"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.Name = raw.Name

	if len(raw.Description) == 0 {
		c.Description = ""
		return nil
	}

	var asString string
	if err := json.Unmarshal(raw.Description, &asString); err == nil {
		c.Description = asString
		return nil
	}

	var asLines []string
	if err := json.Unmarshal(raw.Description, &asLines); err != nil {
		return fmt.Errorf("chore %q: description must be a string or an array of strings: %w", raw.Name, err)
	}
	c.Description = strings.Join(asLines, "\n")
	return nil
}

// ParseChoresBootstrap unmarshals CONFIG_DIR/chores.json's contents - a JSON
// array of ChoreBootstrap entries. Per-entry validation (missing name)
// happens later in BootstrapChores, not here.
func ParseChoresBootstrap(raw string) ([]ChoreBootstrap, error) {
	var entries []ChoreBootstrap
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parsing chores.json: %w", err)
	}
	return entries, nil
}

// AssignmentBootstrap is one chore assignment resolved from
// CONFIG_DIR/assignments.json - a mapping of a catalog chore (matched by
// name against chores.json/the Chore Catalog) to a child (matched by name
// against children.json/the Children card) on a schedule. Exactly one of
// DaysOfWeek/OneOffDate should be set, mirroring the recurring_xor_one_off
// database constraint. See internal/handlers/bootstrap_config.go's
// BootstrapAssignments for how these are reconciled against the database on
// every startup.
type AssignmentBootstrap struct {
	Child string `json:"child"`
	Chore string `json:"chore"`
	// Points defaults to 1 if zero/unset (same default as the dashboard's
	// "Add Chore" form).
	Points int `json:"points"`
	// DaysOfWeek entries are day names, case-insensitive - full ("sunday")
	// or three-letter abbreviations ("sun") - resolved by ResolveWeekday.
	DaysOfWeek []string `json:"days_of_week"`
	// OneOffDate is "YYYY-MM-DD". Mutually exclusive with DaysOfWeek.
	OneOffDate string `json:"one_off_date"`
}

// assignmentBootstrapChore is one entry in an assignments.json child's chore
// list - AssignmentBootstrap minus Child, which comes from the enclosing
// object key instead.
type assignmentBootstrapChore struct {
	Chore      string   `json:"chore"`
	Points     int      `json:"points"`
	DaysOfWeek []string `json:"days_of_week"`
	OneOffDate string   `json:"one_off_date"`
}

// ParseAssignmentsBootstrap unmarshals CONFIG_DIR/assignments.json's
// contents - a JSON object keyed by child name (matched against
// children.json/the Children card), each value a JSON array of that child's
// chore assignments, e.g.:
//
//	{
//	  "Alex": [
//	    {"chore": "Take out trash", "points": 5, "days_of_week": ["tue", "fri"]}
//	  ],
//	  "Sam": [
//	    {"chore": "Feed the dog", "points": 2, "one_off_date": "2026-08-01"}
//	  ]
//	}
//
// Child names are visited in sorted order so results (and any log output
// from BootstrapAssignments) are deterministic run to run. Per-entry
// validation (unknown child/chore, invalid schedule) happens later in
// BootstrapAssignments, not here.
func ParseAssignmentsBootstrap(raw string) ([]AssignmentBootstrap, error) {
	var byChild map[string][]assignmentBootstrapChore
	if err := json.Unmarshal([]byte(raw), &byChild); err != nil {
		return nil, fmt.Errorf("parsing assignments.json: %w", err)
	}

	children := make([]string, 0, len(byChild))
	for child := range byChild {
		children = append(children, child)
	}
	sort.Strings(children)

	var entries []AssignmentBootstrap
	for _, child := range children {
		for _, c := range byChild[child] {
			entries = append(entries, AssignmentBootstrap{
				Child:      child,
				Chore:      c.Chore,
				Points:     c.Points,
				DaysOfWeek: c.DaysOfWeek,
				OneOffDate: c.OneOffDate,
			})
		}
	}
	return entries, nil
}

// ResolveWeekday maps a day name (case-insensitive, full or three-letter
// abbreviation) from an AssignmentBootstrap.DaysOfWeek entry to time.Weekday.
func ResolveWeekday(s string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return 0, fmt.Errorf("unknown day of week %q (expected sun..sat or sunday..saturday)", s)
	}
}
