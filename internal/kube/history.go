package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A direct app's deploys are kept in its namespace, one ConfigMap each
// (mcloud-deploy-<number>), holding what was applied: Knative keeps old
// revisions of HTTP functions, but scheduled and queue functions (a CronJob,
// a ScaledJob or Deployment) have no history of their own. mcloud rollback
// applies one again.
const (
	// HistoryLabel marks the ConfigMaps.
	HistoryLabel = "maxcloud.gronblom.dev/deploy-history"
	// HistoryKeep is how many deploys are kept.
	HistoryKeep = 10

	historyPrefix  = "mcloud-deploy-"
	historyRecord  = "deploy.json"
	historyObjects = "objects.json"
)

// DirectDeploy is one direct deploy (or rollback).
type DirectDeploy struct {
	Number int       `json:"number"`
	Time   time.Time `json:"time"`
	Image  string    `json:"image"`
	// By is who deployed: the git user.email of the working tree, if any.
	By string `json:"by,omitempty"`
	// Commit is the working tree's HEAD; Dirty says it had changes.
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Functions are kind/name of what was applied.
	Functions []string `json:"functions"`
	// Migrations the image applies when it starts (nil: unknown).
	Migrations []string `json:"migrations"`
	// RollbackOf is the deploy this one applied again.
	RollbackOf int `json:"rollbackOf,omitempty"`
}

// RecordDeploy keeps d and the objects it applied as the app's newest
// deploy, numbered after the last, and drops the oldest beyond HistoryKeep.
// Returns its number.
func (c *Client) RecordDeploy(ctx context.Context, app string, d DirectDeploy, objs []map[string]any) (int, error) {
	old, err := c.DirectDeploys(ctx, app)
	if err != nil {
		return 0, err
	}
	d.Number = 1
	if len(old) > 0 {
		d.Number = old[0].Number + 1
	}
	rec, err := json.Marshal(d)
	if err != nil {
		return 0, err
	}
	obj, err := json.Marshal(objs)
	if err != nil {
		return 0, err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s%d", historyPrefix, d.Number), Namespace: app,
			Labels: map[string]string{HistoryLabel: "true", "app.kubernetes.io/managed-by": "mcloud-history"},
		},
		Data: map[string]string{historyRecord: string(rec), historyObjects: string(obj)},
	}
	if _, err := c.Core.CoreV1().ConfigMaps(app).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return 0, err
	}
	for i := HistoryKeep - 1; i < len(old); i++ {
		err := c.Core.CoreV1().ConfigMaps(app).Delete(ctx, fmt.Sprintf("%s%d", historyPrefix, old[i].Number), metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return d.Number, err
		}
	}
	return d.Number, nil
}

// DirectDeploys lists the app's recorded direct deploys, newest first.
func (c *Client) DirectDeploys(ctx context.Context, app string) ([]DirectDeploy, error) {
	l, err := c.Core.CoreV1().ConfigMaps(app).List(ctx, metav1.ListOptions{LabelSelector: HistoryLabel + "=true"})
	if err != nil {
		return nil, err
	}
	var out []DirectDeploy
	for _, cm := range l.Items {
		n, err := strconv.Atoi(strings.TrimPrefix(cm.Name, historyPrefix))
		if err != nil || !strings.HasPrefix(cm.Name, historyPrefix) {
			continue
		}
		var d DirectDeploy
		if err := json.Unmarshal([]byte(cm.Data[historyRecord]), &d); err != nil {
			continue
		}
		d.Number = n
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

// DeployObjects reads the objects a recorded deploy applied.
func (c *Client) DeployObjects(ctx context.Context, app string, number int) ([]map[string]any, error) {
	cm, err := c.Core.CoreV1().ConfigMaps(app).Get(ctx, fmt.Sprintf("%s%d", historyPrefix, number), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("no deploy %d of %s (mcloud history lists them)", number, app)
	}
	if err != nil {
		return nil, err
	}
	var objs []map[string]any
	if err := json.Unmarshal([]byte(cm.Data[historyObjects]), &objs); err != nil {
		return nil, fmt.Errorf("deploy %d: %w", number, err)
	}
	return objs, nil
}

// AppliedMigrations lists the migrations the app's database has applied
// (table maxcloud_migrations). ok is false when there's no such table.
func (c *Client) AppliedMigrations(ctx context.Context, app string) (names []string, ok bool, err error) {
	out, err := c.Psql(ctx, app, "db", `SELECT to_regclass('maxcloud_migrations') IS NOT NULL;
SELECT '---';
SELECT name FROM maxcloud_migrations ORDER BY name;`, nil)
	if err != nil {
		if strings.Contains(err.Error(), "maxcloud_migrations") && strings.Contains(err.Error(), "does not exist") {
			return nil, false, nil
		}
		return nil, false, err
	}
	head, rest, _ := strings.Cut(out, "---\n")
	if strings.TrimSpace(head) != "t" {
		return nil, false, nil
	}
	for _, l := range strings.Split(rest, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, true, nil
}
