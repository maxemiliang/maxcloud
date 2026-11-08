package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

var (
	// rollout is when the controller first kept records in these tests.
	rollout = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	before  = rollout.Add(-48 * time.Hour)
	after   = rollout.Add(time.Hour)
)

// registeredEntry is an mcloud entry for app with a registration ID,
// created at t.
func registeredEntry(app, id string, t time.Time, annotations ...string) *unstructured.Unstructured {
	u := entry(app, mcloudLabels)
	u.SetCreationTimestamp(metav1.NewTime(t))
	a := map[string]string{}
	if id != "" {
		a[RegistrationAnnotation] = id
	}
	for i := 0; i+1 < len(annotations); i += 2 {
		a[annotations[i]] = annotations[i+1]
	}
	u.SetAnnotations(a)
	return u
}

// withRegistrations turns the check on for the env's reconcilers, with
// records beginning at rollout.
func (e *dbEnv) withRegistrations() *Registrations {
	e.storage.buckets[registryBucket] = objectstorage.BucketMetrics{}
	e.objects.put(registryBucket, registrationsSince, `{"since":"`+rollout.Format(time.RFC3339)+`"}`)
	reg := &Registrations{Client: e.client, Storage: e.storage, Objects: e.objects, Bucket: registryBucket,
		Now: func() time.Time { return after.Add(time.Hour) }}
	e.r.Registrations = reg
	e.dr.Registrations = reg
	return reg
}

func (e *dbEnv) record(app string) (Registration, bool) {
	e.t.Helper()
	data, ok := e.objects.objects[registryBucket][registrationKey(app)]
	if !ok {
		return Registration{}, false
	}
	var rec Registration
	if err := json.Unmarshal(data, &rec); err != nil {
		e.t.Fatal(err)
	}
	return rec, true
}

func (e *dbEnv) putRecord(app, id string) {
	e.objects.put(registryBucket, registrationKey(app), `{"app":"`+app+`","id":"`+id+`","since":"2026-11-01T00:00:00Z"}`)
}

func (e *dbEnv) check(reg *Registrations, app string) Verdict {
	e.t.Helper()
	v, err := reg.Check(context.Background(), app)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func TestRegistrationNewAppTakesTheName(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "aaa", after))
	reg := e.withRegistrations()

	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatalf("new app refused: %+v", v)
	}
	rec, ok := e.record("shop")
	if !ok || rec.ID != "aaa" || rec.App != "shop" {
		t.Fatalf("record = %+v, %v", rec, ok)
	}
	// Once recorded, no bucket listing.
	e.storage.calls = nil
	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatalf("second check: %+v", v)
	}
	for _, c := range e.storage.calls {
		if strings.HasPrefix(c, "Buckets") {
			t.Errorf("listed buckets for a recorded registration: %v", e.storage.calls)
		}
	}
}

func TestRegistrationNewEntryDoesNotGetOldData(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "bbb", after))
	reg := e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.storage.buckets["mc-shop--uploads"] = objectstorage.BucketMetrics{}

	v := e.check(reg, "shop")
	if v.Allowed || v.Wait {
		t.Fatalf("got the old registration's data: %+v", v)
	}
	for _, want := range []string{"registration aaa", "registration bbb", AdoptAnnotation + "=aaa", "mc-shop--*"} {
		if !strings.Contains(v.Reason, want) {
			t.Errorf("reason lacks %q: %s", want, v.Reason)
		}
	}
	if rec, _ := e.record("shop"); rec.ID != "aaa" {
		t.Errorf("record moved to %s", rec.ID)
	}
}

func TestRegistrationAdminAdopts(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "bbb", after, AdoptAnnotation, "aaa"))
	reg := e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.storage.buckets["mc-shop--uploads"] = objectstorage.BucketMetrics{}

	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatalf("adoption refused: %+v", v)
	}
	rec, _ := e.record("shop")
	if rec.ID != "bbb" || len(rec.Previous) != 1 || rec.Previous[0] != "aaa" {
		t.Errorf("record = %+v", rec)
	}
}

func TestRegistrationAdoptMustNameTheRecordedID(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "bbb", after, AdoptAnnotation, "zzz"))
	reg := e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.storage.buckets["mc-shop--db"] = objectstorage.BucketMetrics{}

	if v := e.check(reg, "shop"); v.Allowed {
		t.Fatal("adopted with the wrong ID")
	}
}

func TestRegistrationOldDataGone(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "bbb", after))
	reg := e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.storage.buckets["mc-shopping--uploads"] = objectstorage.BucketMetrics{} // another app's

	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatalf("refused with no data left: %+v", v)
	}
	if rec, _ := e.record("shop"); rec.ID != "bbb" {
		t.Errorf("record = %+v", rec)
	}
}

func TestRegistrationUnrecordedData(t *testing.T) {
	// Live when records began: keeps its data.
	e := newDBEnv(t, registeredEntry("shop", "aaa", before), registeredEntry("cart", "ccc", after))
	reg := e.withRegistrations()
	e.storage.buckets["mc-shop--db"] = objectstorage.BucketMetrics{}
	e.storage.buckets["mc-cart--db"] = objectstorage.BucketMetrics{}

	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatalf("existing app refused: %+v", v)
	}
	if rec, _ := e.record("shop"); rec.ID != "aaa" {
		t.Errorf("record = %+v", rec)
	}

	// Made after, over leftovers of an app deleted before records began.
	v := e.check(reg, "cart")
	if v.Allowed || !strings.Contains(v.Reason, AdoptAnnotation+"="+AdoptUnrecorded) {
		t.Fatalf("new entry got unrecorded data: %+v", v)
	}
	if _, ok := e.record("cart"); ok {
		t.Error("recorded a refused registration")
	}
}

func TestRegistrationAdoptUnrecorded(t *testing.T) {
	e := newDBEnv(t, registeredEntry("cart", "ccc", after, AdoptAnnotation, AdoptUnrecorded))
	reg := e.withRegistrations()
	e.storage.buckets["mc-cart--db"] = objectstorage.BucketMetrics{}
	if v := e.check(reg, "cart"); !v.Allowed {
		t.Fatalf("refused: %+v", v)
	}
	// "unrecorded" doesn't adopt a recorded registration.
	e2 := newDBEnv(t, registeredEntry("cart", "ddd", after, AdoptAnnotation, AdoptUnrecorded))
	reg2 := e2.withRegistrations()
	e2.storage.buckets["mc-cart--db"] = objectstorage.BucketMetrics{}
	e2.putRecord("cart", "ccc")
	if v := e2.check(reg2, "cart"); v.Allowed {
		t.Fatal("unrecorded adopted a recorded registration")
	}
}

func TestRegistrationSinceWrittenOnce(t *testing.T) {
	e := newDBEnv(t)
	e.storage.buckets[registryBucket] = objectstorage.BucketMetrics{}
	now := rollout
	reg := &Registrations{Client: e.client, Storage: e.storage, Objects: e.objects, Bucket: registryBucket, Now: func() time.Time { return now }}
	if err := reg.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = after
	got, err := reg.since(context.Background())
	if err != nil || !got.Equal(rollout) {
		t.Fatalf("since = %v, %v", got, err)
	}
}

func TestRegistrationWaitsAndRefuses(t *testing.T) {
	gitEntry := entry("demo", map[string]string{appLabel: "true"})
	deleting := registeredEntry("gone", "ggg", before)
	deleting.SetFinalizers([]string{RegistryFinalizer})
	deleting.SetDeletionTimestamp(&metav1.Time{Time: after})
	e := newDBEnv(t, registeredEntry("shop", "", after), gitEntry, deleting)
	reg := e.withRegistrations()

	if v := e.check(reg, "shop"); v.Allowed || !v.Wait {
		t.Errorf("mcloud entry without an ID: %+v", v)
	}
	if v := e.check(reg, "gone"); v.Allowed || !v.Wait {
		t.Errorf("entry being deleted: %+v", v)
	}
	if v := e.check(reg, "demo"); v.Allowed || v.Wait || !strings.Contains(v.Reason, "from git") {
		t.Errorf("git entry without an ID: %+v", v)
	}
	if v := e.check(reg, "nope"); v.Allowed || v.Wait || !strings.Contains(v.Reason, "no registry entry") {
		t.Errorf("no entry: %+v", v)
	}
	var nilReg *Registrations
	if v := e.check(nilReg, "nope"); !v.Allowed {
		t.Errorf("nil Registrations: %+v", v)
	}
}

func TestRegistrationBadRecordFailsClosed(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "aaa", before))
	reg := e.withRegistrations()
	e.objects.put(registryBucket, registrationKey("shop"), "not json")
	if _, err := reg.Check(context.Background(), "shop"); err == nil {
		t.Fatal("an unreadable record passed")
	}
	if string(e.objects.objects[registryBucket][registrationKey("shop")]) != "not json" {
		t.Error("overwrote an unreadable record")
	}
}

func TestBucketRefusedForAnotherRegistration(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), registeredEntry("shop", "bbb", after), bucket("shop", "uploads"))
	e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.storage.buckets["mc-shop--uploads"] = objectstorage.BucketMetrics{}
	e.storage.calls = nil

	e.mustReconcile("shop", "uploads")
	b := e.bucket("shop", "uploads")
	ready := meta.FindStatusCondition(b.Status.Conditions, "Ready")
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonRegistration {
		t.Fatalf("Ready = %+v", ready)
	}
	if len(b.Finalizers) != 0 {
		t.Errorf("finalizers = %v", b.Finalizers)
	}
	for _, c := range e.storage.calls {
		if !strings.HasPrefix(c, "Buckets") {
			t.Errorf("touched Object Storage: %v", e.storage.calls)
		}
	}
	if _, ok := e.secret("shop"); ok {
		t.Error("handed out the app's credentials")
	}

	// An admin adopts: the bucket comes up.
	u, _ := e.getEntry("shop")
	u.SetAnnotations(map[string]string{RegistrationAnnotation: "bbb", AdoptAnnotation: "aaa"})
	if err := e.client.Update(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.mustReconcile("shop", "uploads")
	if ready := meta.FindStatusCondition(e.bucket("shop", "uploads").Status.Conditions, "Ready"); ready.Status != metav1.ConditionTrue {
		t.Fatalf("after adopting: %+v", ready)
	}
}

func TestBucketWaitsForTheID(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), registeredEntry("shop", "", after), bucket("shop", "uploads"))
	e.withRegistrations()
	res := e.mustReconcile("shop", "uploads")
	if res.RequeueAfter != registrationWait {
		t.Errorf("requeue = %v", res.RequeueAfter)
	}
	if len(e.bucket("shop", "uploads").Status.Conditions) != 0 {
		t.Error("status changed while waiting")
	}
}

func TestDatabaseRefusedForAnotherRegistration(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"), registeredEntry("shop", "bbb", after), database("shop"))
	e.withRegistrations()
	e.putRecord("shop", "aaa")
	e.objects.put(dbBucket, markerKey, `{"generation":3,"state":"ready","updated":"2026-11-01T00:00:00Z"}`)
	e.objects.put(dbBucket, "db-3/base/x/backup.info", "status=DONE\nend_time=2026-11-01 00:00:00+00:00\n")

	e.reconcileDB()
	db := e.db()
	if db.Status.Phase != v1.PhaseBlocked {
		t.Errorf("phase = %s", db.Status.Phase)
	}
	if ready := meta.FindStatusCondition(db.Status.Conditions, "Ready"); ready == nil || ready.Reason != ReasonRegistration {
		t.Errorf("Ready = %+v", ready)
	}
	if _, ok := e.get(clusterGVK, "db"); ok {
		t.Error("restored the old registration's database")
	}
	if !strings.Contains(string(e.objects.objects[dbBucket][markerKey]), `"generation":3`) {
		t.Error("marker changed")
	}
}

// The first rollout: an app live before records began has no ID yet. The
// registry gives it one, and the database carries on from its backups
// (restore, not a new empty database).
func TestRolloutExistingAppKeepsItsData(t *testing.T) {
	legacy := registeredEntry("shop", "", before)
	legacy.SetFinalizers([]string{RegistryFinalizer})
	e := newDBEnv(t, tenantNS("shop"), legacy, database("shop"))
	e.withRegistrations()
	e.objects.put(dbBucket, markerKey, `{"generation":3,"state":"ready","updated":"2026-11-01T00:00:00Z"}`)
	e.objects.put(dbBucket, "db-3/base/x/backup.info", "status=DONE\nend_time=2026-11-01 00:00:00+00:00\n")

	if res := e.reconcileDB(); res.RequeueAfter != registrationWait {
		t.Fatalf("before the ID: %+v", res)
	}
	e.reconcileEntry(e.registry(), "shop")
	u, _ := e.getEntry("shop")
	id := u.GetAnnotations()[RegistrationAnnotation]
	if len(id) != 32 {
		t.Fatalf("registration ID = %q", id)
	}

	e.reconcileDB()
	c, ok := e.get(clusterGVK, "db")
	if !ok {
		t.Fatal("no Cluster")
	}
	if src, _, _ := unstructured.NestedString(c.Object, "spec", "bootstrap", "recovery", "source"); src == "" {
		t.Errorf("not restored from its backups: %v", c.Object["spec"].(map[string]any)["bootstrap"])
	}
	if rec, _ := e.record("shop"); rec.ID != id {
		t.Errorf("record = %+v", rec)
	}
}

func TestRegistryGivesMcloudEntriesAnID(t *testing.T) {
	gitEntry := entry("demo", map[string]string{appLabel: "true"})
	e := newDBEnv(t, entry("shop", mcloudLabels), gitEntry)
	r := e.registry()
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.reconcileEntry(r, "shop")
	e.reconcileEntry(r, "demo")

	u, _ := e.getEntry("shop")
	id := u.GetAnnotations()[RegistrationAnnotation]
	if id == "" {
		t.Fatal("no registration ID")
	}
	g, _ := e.getEntry("demo")
	if g.GetAnnotations()[RegistrationAnnotation] != "" {
		t.Error("gave an entry from git an ID")
	}

	// Kept: a second reconcile doesn't change it, and the copy carries it.
	e.reconcileEntry(r, "shop")
	u, _ = e.getEntry("shop")
	if u.GetAnnotations()[RegistrationAnnotation] != id {
		t.Error("ID changed")
	}
	var copied unstructured.Unstructured
	if err := yaml.Unmarshal(e.objects.objects[registryBucket]["apps/shop.yaml"], &copied.Object); err != nil {
		t.Fatal(err)
	}
	if copied.GetAnnotations()[RegistrationAnnotation] != id || len(copied.GetAnnotations()) != 1 {
		t.Errorf("copy annotations = %v", copied.GetAnnotations())
	}

	// A rebuilt cluster gets the entry back as the same registration.
	stale := u.DeepCopy()
	stale.SetFinalizers(nil)
	if err := e.client.Update(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := e.client.Delete(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := r.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	back, ok := e.getEntry("shop")
	if !ok || back.GetAnnotations()[RegistrationAnnotation] != id {
		t.Fatalf("restored entry = %v", back)
	}
}

func TestRegistryDeleteKeepsTheRecord(t *testing.T) {
	e := newDBEnv(t, registeredEntry("shop", "aaa", before))
	reg := e.withRegistrations()
	r := e.registry()
	e.reconcileEntry(r, "shop")
	if v := e.check(reg, "shop"); !v.Allowed {
		t.Fatal(v.Reason)
	}
	u, _ := e.getEntry("shop")
	if err := e.client.Delete(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	e.reconcileEntry(r, "shop")
	if _, ok := e.getEntry("shop"); ok {
		t.Fatal("entry still there")
	}
	if rec, ok := e.record("shop"); !ok || rec.ID != "aaa" {
		t.Errorf("record = %+v, %v", rec, ok)
	}
}
