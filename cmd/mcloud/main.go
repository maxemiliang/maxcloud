// Command mcloud deploys and runs apps on maxcloud, the homelab's run
// platform.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/maxemiliang/maxcloud/internal/cli"
)

func main() {
	if err := cli.New().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "mcloud:", err)
		os.Exit(1)
	}
}
