package models

import "errors"

// ErrInvalidTransition is returned when a state-changing operation (e.g. marking
// a chore complete, or a parent approving/rejecting) is attempted on a record
// that isn't in the expected starting state — e.g. trying to approve a chore
// that's already been approved, or double-clicking "mark complete" on the kiosk.
var ErrInvalidTransition = errors.New("invalid state transition")
