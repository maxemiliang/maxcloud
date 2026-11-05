package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// fakeObjects is the S3 side: buckets of objects. A bucket exists once
// fakeStorage.EnsureBucket made it (or a test put something in it).
type fakeObjects struct {
	storage *fakeStorage
	objects map[string]map[string][]byte
}

func (f *fakeObjects) exists(bucket string) bool {
	_, ok := f.storage.buckets[bucket]
	return ok
}

func (f *fakeObjects) GetObject(_ context.Context, bucket, key string) ([]byte, bool, error) {
	if !f.exists(bucket) {
		return nil, false, objectstorage.ErrNoSuchBucket
	}
	d, ok := f.objects[bucket][key]
	return d, ok, nil
}

func (f *fakeObjects) PutObject(_ context.Context, bucket, key string, data []byte) error {
	if !f.exists(bucket) {
		return objectstorage.ErrNoSuchBucket
	}
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	f.objects[bucket][key] = data
	return nil
}

func (f *fakeObjects) DeleteObject(_ context.Context, bucket, key string) error {
	if !f.exists(bucket) {
		return objectstorage.ErrNoSuchBucket
	}
	delete(f.objects[bucket], key)
	return nil
}

func (f *fakeObjects) Keys(_ context.Context, bucket, prefix string, max int32) ([]string, error) {
	if !f.exists(bucket) {
		return nil, objectstorage.ErrNoSuchBucket
	}
	var keys []string
	for k := range f.objects[bucket] {
		if strings.HasPrefix(k, prefix) && int32(len(keys)) < max {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// put stores an object as something other than the controller would
// (backups, leftovers), making the bucket if needed.
func (f *fakeObjects) put(bucket, key, data string) {
	f.storage.buckets[bucket] = objectstorage.BucketMetrics{}
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string][]byte{}
	}
	f.objects[bucket][key] = []byte(data)
}

const dbBucket = "mc-shop--db"

type dbEnv struct {
	*env
	objects *fakeObjects
	dr      *DatabaseReconciler
}

func newDBEnv(t *testing.T, objs ...client.Object) *dbEnv {
	e := newEnv(t, objs...)
	o := &fakeObjects{storage: e.storage, objects: map[string]map[string][]byte{}}
	return &dbEnv{env: e, objects: o, dr: &DatabaseReconciler{
		Client:       e.client,
		Identity:     e.identity,
		Storage:      e.storage,
		Objects:      o,
		Endpoint:     "https://example.upcloudobjects.com",
		Image:        "postgres:test",
		StorageClass: "fast-delete",
		Now:          func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) },
	}}
}

func database(ns string, mod ...func(*v1.Database)) *v1.Database {
	db := &v1.Database{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "db", Generation: 1}}
	for _, m := range mod {
		m(db)
	}
	return db
}

func (e *dbEnv) reconcileDB() ctrl.Result {
	e.t.Helper()
	res, err := e.dr.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "shop", Name: "db"}})
	if err != nil {
		e.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (e *dbEnv) db() *v1.Database {
	e.t.Helper()
	var db v1.Database
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: "db"}, &db); err != nil {
		e.t.Fatal(err)
	}
	return &db
}

func (e *dbEnv) get(gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, bool) {
	e.t.Helper()
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	err := e.client.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: name}, u)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return u, true
}

func (e *dbEnv) cluster() *unstructured.Unstructured {
	e.t.Helper()
	c, ok := e.get(clusterGVK, "db")
	if !ok {
		e.t.Fatal("no Cluster db")
	}
	return c
}

func (e *dbEnv) setClusterReady() {
	e.t.Helper()
	c := e.cluster()
	_ = unstructured.SetNestedSlice(c.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if err := e.client.Update(context.Background(), c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dbEnv) deleteCluster() {
	e.t.Helper()
	if err := e.client.Delete(context.Background(), e.cluster()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dbEnv) marker() Marker {
	e.t.Helper()
	data, ok := e.objects.objects[dbBucket][markerKey]
	if !ok {
		e.t.Fatal("no marker")
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *dbEnv) backUp(gen int64) {
	e.backUpAt(gen, time.Date(2026, 11, 5, 7, 0, 0, 0, time.UTC))
}

// backUpAt stores a finished base backup of generation gen that ended at end.
func (e *dbEnv) backUpAt(gen int64, end time.Time) {
	id := end.UTC().Format("20060102T150405")
	info := "backup_label=x\nstatus=DONE\nend_time=" + end.UTC().Format("2006-01-02 15:04:05.000000+00:00") + "\n"
	e.objects.put(dbBucket, serverName(gen)+"/base/"+id+"/backup.info", info)
	e.objects.put(dbBucket, serverName(gen)+"/base/"+id+"/data.tar.gz", "...")
}

func (e *dbEnv) annotate(key, value string) {
	e.t.Helper()
	db := e.db()
	if db.Annotations == nil {
		db.Annotations = map[string]string{}
	}
	db.Annotations[key] = value
	if err := e.client.Update(context.Background(), db); err != nil {
		e.t.Fatal(err)
	}
}

func targetOf(c *unstructured.Unstructured) string {
	t, _, _ := unstructured.NestedString(c.Object, "spec", "bootstrap", "recovery", "recoveryTarget", "targetTime")
	return t
}

func bootstrapOf(c *unstructured.Unstructured) (method, from string) {
	b, _, _ := unstructured.NestedMap(c.Object, "spec", "bootstrap")
	if _, ok := b["initdb"]; ok {
		return "initdb", ""
	}
	ext, _, _ := unstructured.NestedSlice(c.Object, "spec", "externalClusters")
	if len(ext) == 1 {
		from, _, _ = unstructured.NestedString(ext[0].(map[string]any), "plugin", "parameters", "serverName")
	}
	return "recovery", from
}

func condition(db *v1.Database, t string) *metav1.Condition {
	return meta.FindStatusCondition(db.Status.Conditions, t)
}

// The whole life of a database: created, backed up, its Cluster lost,
// restored from the backups into a new generation.
func TestDatabaseLifecycle(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop"))

	// New: bucket, marker, ObjectStore, Cluster with initdb on db-1.
	e.reconcileDB()
	if !e.objects.exists(dbBucket) {
		t.Fatal("no backup bucket")
	}
	if m := e.marker(); m.Generation != 1 || m.State != StateInitializing {
		t.Errorf("marker = %+v", m)
	}
	c := e.cluster()
	if method, _ := bootstrapOf(c); method != "initdb" {
		t.Errorf("bootstrap = %s, want initdb", method)
	}
	if got := clusterServerName(c); got != "db-1" {
		t.Errorf("serverName = %q, want db-1", got)
	}
	if prio, _, _ := unstructured.NestedString(c.Object, "spec", "priorityClassName"); prio != "tenant" {
		t.Errorf("priorityClassName = %q", prio)
	}
	if sc, _, _ := unstructured.NestedString(c.Object, "spec", "storage", "storageClass"); sc != "fast-delete" {
		t.Errorf("storageClass = %q, want the configured one", sc)
	}
	if pdb, _, _ := unstructured.NestedBool(c.Object, "spec", "enablePDB"); pdb {
		t.Error("a single instance shouldn't have a PDB (it would block drains)")
	}
	if owner := metav1.GetControllerOf(c); owner == nil || owner.Kind != "Database" {
		t.Errorf("Cluster owner = %+v", owner)
	}
	os, ok := e.get(objectStoreGVK, "db-backups")
	if !ok {
		t.Fatal("no ObjectStore")
	}
	if dest, _, _ := unstructured.NestedString(os.Object, "spec", "configuration", "destinationPath"); dest != "s3://mc-shop--db/" {
		t.Errorf("destinationPath = %q", dest)
	}
	if _, ok := e.secret("shop"); !ok {
		t.Error("no maxcloud-s3 Secret for the backups")
	}
	if db := e.db(); db.Status.Phase != v1.PhaseCreating || db.Status.Generation != 1 {
		t.Errorf("status = %+v", db.Status)
	}

	// Still starting: not ready, no backups scheduled yet.
	e.reconcileDB()
	if _, ok := e.get(scheduledBackupGVK, "db-daily"); ok {
		t.Error("ScheduledBackup before the Cluster is ready")
	}

	// Ready, no base backup yet: the first one is taken, still not Ready.
	e.setClusterReady()
	e.reconcileDB()
	if _, ok := e.get(backupGVK, "db-gen-1"); !ok {
		t.Error("no first base backup for generation 1")
	}
	if _, ok := e.get(scheduledBackupGVK, "db-daily"); !ok {
		t.Error("no ScheduledBackup")
	}
	if c := condition(e.db(), "Ready"); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %+v, want False until the base backup exists", c)
	}

	// The base backup lands: the generation is ready.
	e.backUp(1)
	e.reconcileDB()
	if m := e.marker(); m.Generation != 1 || m.State != StateReady {
		t.Errorf("marker = %+v", m)
	}
	db := e.db()
	if c := condition(db, "Ready"); c == nil || c.Status != metav1.ConditionTrue || db.Status.Phase != v1.PhaseReady {
		t.Errorf("status = %+v", db.Status)
	}

	// The Cluster is lost: restored from db-1 into db-2.
	e.deleteCluster()
	e.reconcileDB()
	c = e.cluster()
	if method, from := bootstrapOf(c); method != "recovery" || from != "db-1" {
		t.Errorf("bootstrap = %s from %q, want recovery from db-1", method, from)
	}
	if got := clusterServerName(c); got != "db-2" {
		t.Errorf("serverName = %q, want db-2 (never write over db-1)", got)
	}
	if m := e.marker(); m.Generation != 2 || m.State != StateRestoring || m.RestoredFrom != 1 {
		t.Errorf("marker = %+v", m)
	}
	if db := e.db(); db.Status.Phase != v1.PhaseRestoring || db.Status.RestoredFrom != 1 {
		t.Errorf("status = %+v", db.Status)
	}

	// The restore comes up and gets its own base backup.
	e.setClusterReady()
	e.reconcileDB()
	if _, ok := e.get(backupGVK, "db-gen-2"); !ok {
		t.Error("no first base backup for generation 2")
	}
	e.backUp(2)
	e.reconcileDB()
	if m := e.marker(); m.Generation != 2 || m.State != StateReady || m.RestoredFrom != 1 {
		t.Errorf("marker = %+v", m)
	}
	if db := e.db(); db.Status.Phase != v1.PhaseReady {
		t.Errorf("phase = %s", db.Status.Phase)
	}
}

func TestDatabaseBlocksOnUnknownBackups(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop"))
	e.objects.put(dbBucket, "somewhere/base/x/backup.info", "status=DONE")
	e.reconcileDB()
	if _, ok := e.get(clusterGVK, "db"); ok {
		t.Fatal("created a Cluster over backups it can't account for")
	}
	db := e.db()
	if db.Status.Phase != v1.PhaseBlocked {
		t.Errorf("phase = %s, want Blocked", db.Status.Phase)
	}
	if c := condition(db, "Blocked"); c == nil || c.Status != metav1.ConditionTrue || !strings.Contains(c.Message, v1.RecoverFromAnnotation) {
		t.Errorf("Blocked = %+v", c)
	}
}

func TestDatabaseRecoverFromAnnotation(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop", func(db *v1.Database) {
		db.Annotations = map[string]string{v1.RecoverFromAnnotation: "2"}
	}))
	// The marker is lost, generation 2's backups are there.
	e.backUp(2)
	e.reconcileDB()
	c := e.cluster()
	if method, from := bootstrapOf(c); method != "recovery" || from != "db-2" {
		t.Errorf("bootstrap = %s from %q, want recovery from db-2", method, from)
	}
	if got := clusterServerName(c); got != "db-3" {
		t.Errorf("serverName = %q, want db-3", got)
	}
	if _, ok := e.db().Annotations[v1.RecoverFromAnnotation]; ok {
		t.Error("the annotation wasn't removed after acting on it")
	}
}

func TestDatabaseWontAdoptAForeignCluster(t *testing.T) {
	foreign := &unstructured.Unstructured{}
	foreign.SetGroupVersionKind(clusterGVK)
	foreign.SetNamespace("shop")
	foreign.SetName("db")
	e := newDBEnv(t, tenantNS("shop"), database("shop"), foreign)
	e.reconcileDB()
	if db := e.db(); db.Status.Phase != v1.PhaseBlocked {
		t.Errorf("phase = %s, want Blocked", db.Status.Phase)
	}
	if e.objects.exists(dbBucket) {
		t.Error("made a backup bucket for someone else's Cluster")
	}
}

func TestDatabaseInTerminatingNamespace(t *testing.T) {
	ns := tenantNS("shop")
	ns.Status.Phase = corev1.NamespaceTerminating
	e := newDBEnv(t, ns, database("shop"))
	e.objects.put(dbBucket, markerKey, string(Marker{Generation: 1, State: StateReady}.json()))
	e.backUp(1)
	e.reconcileDB()
	if _, ok := e.get(clusterGVK, "db"); ok {
		t.Error("restored a Cluster into a namespace being deleted")
	}
	if m := e.marker(); m.Generation != 1 {
		t.Errorf("marker changed: %+v", m)
	}
}

func TestDatabaseScalesAndGrows(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop"))
	e.reconcileDB()

	db := e.db()
	db.Spec.Instances = 2
	db.Spec.Size = resource.MustParse("2Gi")
	if err := e.client.Update(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	e.reconcileDB()
	c := e.cluster()
	if n, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances"); n != 2 {
		t.Errorf("instances = %d", n)
	}
	if pdb, _, _ := unstructured.NestedBool(c.Object, "spec", "enablePDB"); !pdb {
		t.Error("two instances should have a PDB")
	}
	if size, _, _ := unstructured.NestedString(c.Object, "spec", "storage", "size"); size != "2Gi" {
		t.Errorf("size = %s", size)
	}

	// Disks don't shrink.
	db = e.db()
	db.Spec.Size = resource.MustParse("1Gi")
	if err := e.client.Update(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	e.reconcileDB()
	if size, _, _ := unstructured.NestedString(e.cluster().Object, "spec", "storage", "size"); size != "2Gi" {
		t.Errorf("size shrank to %s", size)
	}
	// And it still archives to the same generation.
	if got := clusterServerName(e.cluster()); got != "db-1" {
		t.Errorf("serverName = %q", got)
	}
}

func TestDatabaseDeleteKeepsBackups(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop"))
	e.reconcileDB()
	if err := e.client.Delete(context.Background(), e.db()); err != nil {
		t.Fatal(err)
	}
	e.reconcileDB()
	if !e.objects.exists(dbBucket) {
		t.Error("Retain: the backup bucket was deleted")
	}
	if _, ok := e.storage.users["mc-shop"]; ok {
		t.Error("the app's user is still there with nothing left using it")
	}
	var db v1.Database
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: "db"}, &db); !apierrors.IsNotFound(err) {
		t.Errorf("Database still there: %v", err)
	}
}

// A Bucket and a Database share the app's credentials: deleting one keeps
// them for the other.
func TestIdentitySharedByBucketAndDatabase(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), database("shop"), bucket("shop", "uploads"))
	e.reconcileDB()
	e.mustReconcile("shop", "uploads")
	if e.storage.nextKey != 1 {
		t.Errorf("%d keys made, want 1 shared", e.storage.nextKey)
	}
	e.delete("shop", "uploads")
	e.mustReconcile("shop", "uploads")
	if _, ok := e.secret("shop"); !ok {
		t.Error("the Secret went while the Database still needs it")
	}
}

// A ready database at generation 1: Cluster ready, base backup, marker.
func readyDB(t *testing.T) *dbEnv {
	e := newDBEnv(t, tenantNS("shop"), database("shop"))
	e.reconcileDB()
	e.setClusterReady()
	e.backUpAt(1, time.Date(2026, 11, 5, 7, 44, 0, 0, time.UTC))
	e.reconcileDB()
	if m := e.marker(); m.State != StateReady {
		t.Fatalf("setup: marker %+v", m)
	}
	return e
}

func TestRestoreToAPointInTime(t *testing.T) {
	e := readyDB(t) // now is 2026-11-05T12:00Z in these tests
	e.annotate(v1.RestoreToAnnotation, "2026-11-05T10:30:00Z")

	// Planned in the marker, then the Cluster is taken down.
	e.reconcileDB()
	m := e.marker()
	if m.Generation != 2 || m.State != StateRestoring || m.RestoredFrom != 1 || m.TargetTime == nil ||
		!m.TargetTime.Equal(time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("marker = %+v", m)
	}
	if _, ok := e.get(clusterGVK, "db"); ok {
		t.Fatal("the Cluster wasn't taken down")
	}
	if db := e.db(); db.Status.Phase != v1.PhaseRestoring {
		t.Errorf("phase = %s", db.Status.Phase)
	}

	// Brought back from generation 1, up to that time, into generation 2.
	e.reconcileDB()
	c := e.cluster()
	if method, from := bootstrapOf(c); method != "recovery" || from != "db-1" {
		t.Errorf("bootstrap = %s from %q", method, from)
	}
	if got := targetOf(c); got != "2026-11-05T10:30:00Z" {
		t.Errorf("targetTime = %q", got)
	}
	if got := clusterServerName(c); got != "db-2" {
		t.Errorf("serverName = %q", got)
	}
	db := e.db()
	if _, ok := db.Annotations[v1.RestoreToAnnotation]; ok {
		t.Error("annotation not removed")
	}
	if db.Status.RestoredTo == nil || condition(db, "Restore") == nil || condition(db, "Restore").Status != metav1.ConditionTrue {
		t.Errorf("status = %+v", db.Status)
	}

	// Lost again before its own backup: the same restore again, same target.
	e.deleteCluster()
	e.reconcileDB()
	if got := targetOf(e.cluster()); got != "2026-11-05T10:30:00Z" {
		t.Errorf("retry lost the target: %q", got)
	}
}

func TestRestoreToPicksTheNewestGenerationBeforeIt(t *testing.T) {
	e := readyDB(t)
	// Generation 2 exists from an earlier restore, backed up at 09:00.
	e.deleteCluster()
	e.reconcileDB()
	e.setClusterReady()
	e.backUpAt(2, time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	e.reconcileDB()

	for _, tc := range []struct {
		at   string
		from string
	}{
		{"2026-11-05T08:30:00Z", "db-1"}, // before generation 2's first backup
		{"2026-11-05T11:00:00Z", "db-2"},
	} {
		e.annotate(v1.RestoreToAnnotation, tc.at)
		e.reconcileDB() // plan, take down
		e.reconcileDB() // restore
		if _, from := bootstrapOf(e.cluster()); from != tc.from {
			t.Errorf("restore to %s: from %s, want %s", tc.at, from, tc.from)
		}
		e.setClusterReady()
		gen := e.marker().Generation
		e.backUpAt(gen, time.Date(2026, 11, 5, 11, 30, 0, 0, time.UTC))
		e.reconcileDB()
	}
}

func TestRestoreToRefused(t *testing.T) {
	for _, at := range []string{"2026-11-05T06:00:00Z", "2026-11-06T00:00:00Z", "yesterday"} {
		t.Run(at, func(t *testing.T) {
			e := readyDB(t)
			before := e.cluster().GetUID()
			e.annotate(v1.RestoreToAnnotation, at)
			e.reconcileDB()
			db := e.db()
			if c := condition(db, "Restore"); c == nil || c.Status != metav1.ConditionFalse {
				t.Errorf("Restore = %+v", c)
			}
			if _, ok := db.Annotations[v1.RestoreToAnnotation]; ok {
				t.Error("annotation left behind")
			}
			if e.cluster().GetUID() != before || e.marker().Generation != 1 {
				t.Error("the database was touched")
			}
		})
	}
}

func TestParseBackupInfo(t *testing.T) {
	info := parseBackupInfo([]byte("backup_label=None\nstatus=DONE\nend_time=2026-11-05 07:44:41.123456+00:00\n"))
	if info["status"] != "DONE" {
		t.Errorf("status = %q", info["status"])
	}
	end, err := parseBarmanTime(info["end_time"])
	if err != nil || !end.Equal(time.Date(2026, 11, 5, 7, 44, 41, 123456000, time.UTC)) {
		t.Errorf("end = %v, %v", end, err)
	}
	if _, err := parseBarmanTime("2026-11-05 07:44:41+02:00"); err != nil {
		t.Error(err)
	}
}
