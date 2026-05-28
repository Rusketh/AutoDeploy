package model

import "errors"

// Errors returned by repository methods. Wrap them with fmt.Errorf("...: %w",
// err) so callers can still inspect with errors.Is. The HTTP layer translates
// these to status codes in one place.
var (
	// ErrNotFound — the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict — a uniqueness or referential-integrity violation.
	ErrConflict = errors.New("conflict")
	// ErrInUse — the object has references from other rows and cannot be
	// deleted without resolving them. The caller may surface the reference
	// count to the operator before retrying.
	ErrInUse = errors.New("in use")
	// ErrCycle — saving the proposed image parent would create an
	// inheritance cycle.
	ErrCycle = errors.New("inheritance cycle")
	// ErrValidation — the submitted object failed validation.
	ErrValidation = errors.New("validation failed")
)
