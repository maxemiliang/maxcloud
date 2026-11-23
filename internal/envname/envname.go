// Package envname says which names an app's secret or plain variable may
// have: an environment variable's name that the platform doesn't set
// itself.
//
// The same file is in mcloud (internal/envname) and in the controller
// (controller/internal/envname), which are separate Go modules: mcloud
// checks before it asks for a value or edits a Secret itself, and the
// controller's apps API checks every client's request. A test in mcloud
// fails if the two copies differ.
package envname

import (
	"fmt"
	"regexp"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// Reserved: a name the platform sets itself, which a secret or variable
// would shadow or be shadowed by.
func Reserved(name string) bool {
	return name == "DATABASE_URL" || name == "FUNCTION_TARGET" || name == "PORT" ||
		strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "MAXCLOUD_") || strings.HasPrefix(name, "K_")
}

// Check: name can be a secret or variable of an app's.
func Check(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q: use an environment variable name: A-Z, 0-9 and _, not starting with a digit", name)
	}
	if Reserved(name) {
		return fmt.Errorf("%s: the platform sets that one itself", name)
	}
	return nil
}
