// Command mcloud deploys and runs apps on maxcloud, the homelab's run
// platform.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/maxemiliang/maxcloud/internal/cli"
)

func main() {
	// Run as docker-credential-mcloud (a link mcloud makes while it builds),
	// it's docker's credential helper for the maxcloud registry.
	if filepath.Base(os.Args[0]) == "docker-credential-mcloud" {
		os.Exit(cli.CredentialHelper(os.Args[1:], os.Stdin, os.Stdout))
	}
	if err := cli.New().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "mcloud:", err)
		os.Exit(1)
	}
}
