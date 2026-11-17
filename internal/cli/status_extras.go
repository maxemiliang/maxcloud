package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maxemiliang/maxcloud/internal/appapi"
	"github.com/maxemiliang/maxcloud/internal/kube"
	"github.com/maxemiliang/maxcloud/internal/ui"
)

// printResources shows the app's buckets and custom domains, with why one
// isn't Ready.
func printResources(u *ui.UI, st *kube.Status) {
	if len(st.Buckets) > 0 {
		var rows [][]string
		for _, b := range st.Buckets {
			access := "private"
			if b.Public {
				access = "public"
				if b.URL != "" {
					access += " " + u.Link(b.URL)
				}
			}
			size := "-"
			if b.Size != nil {
				size = humanBytes(*b.Size)
				if b.Objects != nil {
					size += fmt.Sprintf(" (%d objects)", *b.Objects)
				}
			}
			rows = append(rows, []string{u.Accent(b.Name), u.Dim(b.Bucket), access, size, readyCell(u, b.Ready, b.Observed)})
		}
		u.Println()
		u.Table([]string{"Bucket", "In Object Storage", "Access", "Size", "Ready"}, rows)
	}
	if len(st.Domains) > 0 {
		var rows [][]string
		for _, d := range st.Domains {
			url := d.URL
			if url == "" {
				url = "https://" + d.Host + "/"
			}
			proxied := ""
			if d.Proxied {
				proxied = u.Dim(" (Cloudflare)")
			}
			rows = append(rows, []string{u.Link(url) + proxied, d.Service, readyCell(u, d.Ready, d.Observed)})
		}
		u.Println()
		u.Table([]string{"Domain", "Service", "Ready"}, rows)
	}
}

// readyCell is a Ready condition: the mark, and the reason and message
// when it isn't Ready.
func readyCell(u *ui.UI, c kube.Condition, observed bool) string {
	if c.Ready() && observed {
		return u.Mark(c.Status, true)
	}
	if c.Status == "" {
		return u.Mark("", true) + " waiting for the controller"
	}
	cell := u.Mark(c.Status, true)
	if c.Reason != "" {
		cell += " " + c.Reason
	}
	if c.Message != "" {
		cell += ": " + truncate(c.Message, 80)
	}
	return cell
}

// statusAlerts reads the platform's alerts about the app, for mcloud
// status: nil (and nothing said) when the platform doesn't serve them or
// the caller isn't one of the app's owners.
func (g *globals) statusAlerts(ctx context.Context, k *kube.Client, app string) []appapi.Alert {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api, err := g.appAPI(ctx, k, appapi.Alerts)
	if err != nil {
		return nil
	}
	a, err := api.Alerts(ctx, app)
	if err != nil {
		if !errors.Is(err, appapi.ErrForbidden) {
			g.UI().Note("alerts: %v", err)
		}
		return nil
	}
	return a.Alerts
}

// printStatusExtras shows what comes after the problems: the alerts.
func printStatusExtras(u *ui.UI, _ *kube.Status, alerts []appapi.Alert) {
	if len(alerts) == 0 {
		return
	}
	u.Heading("Alerts")
	for _, a := range alerts {
		u.Printf("  %s", alertLine(u, a))
	}
}

// alertLine is one alert on one line.
func alertLine(u *ui.UI, a appapi.Alert) string {
	mark := u.Red("•")
	if a.State != "firing" {
		mark = u.Amber("•")
	}
	text := a.Summary
	if text == "" {
		text = a.Name
	}
	line := mark + " " + text
	var meta []string
	if a.Summary != "" {
		meta = append(meta, a.Name)
	}
	if a.State != "firing" {
		meta = append(meta, a.State)
	}
	if a.ActiveAt != nil {
		meta = append(meta, "since "+agoString(*a.ActiveAt))
	}
	if len(meta) > 0 {
		line += u.Dim(" (" + strings.Join(meta, ", ") + ")")
	}
	return line
}

// shortSHA is the first 7 characters of a commit (from Flux's
// "main@sha1:<sha>" too).
func shortSHA(rev string) string {
	if _, sha, ok := strings.Cut(rev, "sha1:"); ok {
		rev = sha
	}
	if len(rev) > 7 {
		return rev[:7]
	}
	if rev == "" {
		return "?"
	}
	return rev
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// agoString is how long ago t was, roughly.
func agoString(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
