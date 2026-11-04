// A small app for the end-to-end test.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/maxemiliang/maxcloud/functions"
)

func main() {
	functions.HTTPFunc("hello", func(w http.ResponseWriter, r *http.Request) {
		ms, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		time.Sleep(time.Duration(ms) * time.Millisecond)
		fmt.Fprintln(w, "hello")
	}, functions.Public())
	functions.Schedule("nightly-report", "0 3 * * *", func(context.Context) error {
		if os.Getenv("FAIL") != "" {
			return fmt.Errorf("report failed")
		}
		return nil
	})
	functions.Queue("resize", "images", func(context.Context, *functions.Job) error { return nil })
	functions.Start()
}
