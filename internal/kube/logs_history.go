package kube

import (
	"context"
	"io"
	"regexp"
)

// LogPodPattern is a regular expression for the names of the pods of an
// app's service, Deployment, CronJob, ScaledJob or database NAME, past
// ones included: what a query to Loki (which has only the pod's name and
// namespace) filters on. Pods of worker-two don't match worker.
func (c *Client) LogPodPattern(ctx context.Context, ns, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	q := regexp.QuoteMeta(name)
	ksvc, err := c.get(ctx, gvrKService, ns, name)
	if err != nil {
		return "", err
	}
	if ksvc != nil {
		// <service>-00001-deployment-<hash>-<id>
		return q + `-[0-9]+-deployment-.+`, nil
	}
	// Deployment and ScaledObject: <name>-<replicaset hash>-<id>;
	// CronJob: <name>-<minute>-<id>; ScaledJob: <name>-<id>-<id>;
	// CloudNativePG: <name>-<n> and <name>-<n>-initdb-<id>.
	return q + `-([0-9]+(-[a-z0-9-]+)?|[a-z0-9]{5,10}-[a-z0-9]{5})`, nil
}

// LogPrinter prints log lines the way Logs does: "<prefix> <line>", the
// prefix colored per pod on a terminal.
type LogPrinter struct{ w *lineWriter }

// NewLogPrinter prints to w; notes go to errW.
func NewLogPrinter(w, errW io.Writer, color bool) *LogPrinter {
	return &LogPrinter{w: &lineWriter{w: w, errW: errW, color: color}}
}

// Line prints one line.
func (p *LogPrinter) Line(prefix, text string) { p.w.line(prefix, text) }
