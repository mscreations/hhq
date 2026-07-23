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

import "errors"

// ErrInvalidTransition is returned when a state-changing operation (e.g. marking
// a chore complete, or a parent approving/rejecting) is attempted on a record
// that isn't in the expected starting state — e.g. trying to approve a chore
// that's already been approved, or double-clicking "mark complete" on the kiosk.
var ErrInvalidTransition = errors.New("invalid state transition")
