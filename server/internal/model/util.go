package model

import (
	"fmt"
	"regexp"
	"strings"
)

// nameRE accepts the conservative set of name characters AutoDeploy allows
// for artifact and image names: letters, digits, dash, underscore, dot,
// space. 1-128 characters.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9._ \-]{1,128}$`)

func validateName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name required", ErrValidation)
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: name %q contains invalid characters or is too long", ErrValidation, name)
	}
	return nil
}

// isUniqueErr reports whether the underlying SQLite error indicates a
// uniqueness violation. SQLite reports these as "UNIQUE constraint failed"
// strings; the modernc driver does not expose a typed error.
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
