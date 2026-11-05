package controller

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/naming"
)

// The CloudNativePG objects a Database becomes. Unstructured, so the
// controller doesn't pull in CloudNativePG's Go API for three objects.

var (
	clusterGVK         = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	scheduledBackupGVK = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackup"}
	backupGVK          = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Backup"}
	objectStoreGVK     = schema.GroupVersionKind{Group: "barmancloud.cnpg.io", Version: "v1", Kind: "ObjectStore"}
)

const (
	barmanPlugin = "barman-cloud.cloudnative-pg.io"
	// TenantPriorityClass: every pod in an app's namespace must use it
	// (homelab infrastructure/configs/run).
	tenantPriorityClass = "tenant"
	// The database and its owner inside Postgres; CloudNativePG puts the
	// owner's connection details in the Secret <cluster>-app.
	appDatabase = "app"
)

func newUnstructured(gvk schema.GroupVersionKind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetLabels(map[string]string{ManagedByLabel: managedBy})
	return u
}

func objectStoreName(db *v1.Database) string     { return db.Name + "-backups" }
func scheduledBackupName(db *v1.Database) string { return db.Name + "-daily" }

// objectStore is where the database's backups go: the app's bucket
// mc-<app>--db, with the app's own credentials (the Secret maxcloud-s3).
// Each generation is a folder in it, chosen by the Cluster's serverName.
func objectStore(db *v1.Database, bucket, endpoint string) *unstructured.Unstructured {
	u := newUnstructured(objectStoreGVK, db.Namespace, objectStoreName(db))
	checksums := []any{
		// UpCloud rejects the checksums newer AWS SDKs send by default.
		map[string]any{"name": "AWS_REQUEST_CHECKSUM_CALCULATION", "value": "when_required"},
		map[string]any{"name": "AWS_RESPONSE_CHECKSUM_VALIDATION", "value": "when_required"},
	}
	u.Object["spec"] = map[string]any{
		"retentionPolicy": db.Spec.Backup.Retention,
		"configuration": map[string]any{
			"destinationPath": "s3://" + bucket + "/",
			"endpointURL":     endpoint,
			"s3Credentials": map[string]any{
				"accessKeyId":     map[string]any{"name": naming.SecretName, "key": "AWS_ACCESS_KEY_ID"},
				"secretAccessKey": map[string]any{"name": naming.SecretName, "key": "AWS_SECRET_ACCESS_KEY"},
				"region":          map[string]any{"name": naming.SecretName, "key": "AWS_REGION"},
			},
			"wal":  map[string]any{"compression": "zstd"},
			"data": map[string]any{"compression": "gzip"},
		},
		"instanceSidecarConfiguration": map[string]any{
			"env": checksums,
			"resources": map[string]any{
				"requests": map[string]any{"cpu": "10m", "memory": "48Mi"},
				"limits":   map[string]any{"memory": "256Mi"},
			},
		},
	}
	return u
}

// scheduledBackup takes the daily base backups. Not immediate: that only
// fires when the ScheduledBackup is created, and it outlives a Cluster.
// Each new generation gets its first base backup from firstBackup instead.
func scheduledBackup(db *v1.Database) *unstructured.Unstructured {
	u := newUnstructured(scheduledBackupGVK, db.Namespace, scheduledBackupName(db))
	u.Object["spec"] = map[string]any{
		"cluster":              map[string]any{"name": db.Name},
		"schedule":             db.Spec.Backup.Schedule,
		"immediate":            false,
		"backupOwnerReference": "self",
		"method":               "plugin",
		"pluginConfiguration":  map[string]any{"name": barmanPlugin},
	}
	return u
}

// firstBackup is generation gen's first base backup, taken as soon as its
// Cluster is ready: until it exists, the generation can't be restored.
func firstBackup(db *v1.Database, gen int64) *unstructured.Unstructured {
	u := newUnstructured(backupGVK, db.Namespace, fmt.Sprintf("%s-gen-%d", db.Name, gen))
	u.Object["spec"] = map[string]any{
		"cluster":             map[string]any{"name": db.Name},
		"method":              "plugin",
		"pluginConfiguration": map[string]any{"name": barmanPlugin},
	}
	return u
}

// cluster is the database's CloudNativePG Cluster, writing its backups to
// generation gen. from > 0 restores generation from instead of starting
// empty.
func cluster(db *v1.Database, image string, gen, from int64) *unstructured.Unstructured {
	u := newUnstructured(clusterGVK, db.Namespace, db.Name)
	spec := map[string]any{
		"instances":         int64(db.Spec.Instances),
		"imageName":         image,
		"priorityClassName": tenantPriorityClass,
		// One instance can't keep a PodDisruptionBudget without blocking
		// every node drain; it goes down for the minute a drain takes.
		"enablePDB": db.Spec.Instances > 1,
		"storage": map[string]any{
			"size":               db.Spec.Size.String(),
			"resizeInUseVolumes": true,
		},
		"resources": resources(db),
		"postgresql": map[string]any{
			"parameters": map[string]any{
				// Small: these are apps' databases on a small cluster.
				"shared_buffers": "64MB",
				"max_wal_size":   "512MB",
				// A quiet database still archives WAL every 5 minutes, so
				// a restore loses at most that much.
				"archive_timeout": "5min",
			},
		},
		"plugins": []any{map[string]any{
			"name":          barmanPlugin,
			"isWALArchiver": true,
			"parameters": map[string]any{
				"barmanObjectName": objectStoreName(db),
				"serverName":       serverName(gen),
			},
		}},
	}
	if from == 0 {
		spec["bootstrap"] = map[string]any{
			"initdb": map[string]any{"database": appDatabase, "owner": appDatabase},
		}
	} else {
		spec["bootstrap"] = map[string]any{
			"recovery": map[string]any{
				"source":   "origin",
				"database": appDatabase,
				"owner":    appDatabase,
			},
		}
		spec["externalClusters"] = []any{map[string]any{
			"name": "origin",
			"plugin": map[string]any{
				"name": barmanPlugin,
				"parameters": map[string]any{
					"barmanObjectName": objectStoreName(db),
					"serverName":       serverName(from),
				},
			},
		}}
	}
	u.Object["spec"] = spec
	return u
}

func resources(db *v1.Database) map[string]any {
	if r := db.Spec.Resources; r != nil {
		out := map[string]any{}
		if len(r.Requests) > 0 {
			req := map[string]any{}
			for k, v := range r.Requests {
				req[string(k)] = v.String()
			}
			out["requests"] = req
		}
		if len(r.Limits) > 0 {
			lim := map[string]any{}
			for k, v := range r.Limits {
				lim[string(k)] = v.String()
			}
			out["limits"] = lim
		}
		return out
	}
	return map[string]any{
		"requests": map[string]any{"cpu": "20m", "memory": "128Mi"},
		"limits":   map[string]any{"memory": "512Mi"},
	}
}

// clusterServerName is the generation folder an existing Cluster archives
// to, "" if it has none of ours.
func clusterServerName(c *unstructured.Unstructured) string {
	plugins, _, _ := unstructured.NestedSlice(c.Object, "spec", "plugins")
	for _, p := range plugins {
		pm, ok := p.(map[string]any)
		if !ok || pm["name"] != barmanPlugin {
			continue
		}
		s, _, _ := unstructured.NestedString(pm, "parameters", "serverName")
		return s
	}
	return ""
}

// clusterReady reports whether CloudNativePG says the Cluster is Ready.
func clusterReady(c *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(c.Object, "status", "conditions")
	for _, x := range conds {
		cm, ok := x.(map[string]any)
		if ok && cm["type"] == "Ready" && cm["status"] == "True" {
			return true
		}
	}
	return false
}
