package functions

import (
	"fmt"
	"os"
)

// Secret returns the app's secret name (mcloud secrets set NAME), which
// reaches every function as an environment variable, or an error that says
// how to set it. Read it where it's used, not at package init:
// MAXCLOUD_DESCRIBE runs the app without its secrets.
func Secret(name string) (string, error) {
	return secret(name, os.Getenv)
}

func secret(name string, getenv func(string) string) (string, error) {
	if v := getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("secret %s isn't set: mcloud secrets set %s (in the app's repo), then deploy", name, name)
}
