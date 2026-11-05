package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
)

func (e *dbEnv) branch(name string, at *time.Time) *BranchReconciler {
	e.t.Helper()
	b := &v1.DatabaseBranch{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, Generation: 1}}
	if at != nil {
		b.Spec.At = &metav1.Time{Time: *at}
	}
	if err := e.client.Create(context.Background(), b); err != nil {
		e.t.Fatal(err)
	}
	return &BranchReconciler{Client: e.client, DB: e.dr}
}

func (e *dbEnv) reconcileBranch(r *BranchReconciler, name string) *v1.DatabaseBranch {
	e.t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "shop", Name: name}}); err != nil {
		e.t.Fatalf("reconcile branch: %v", err)
	}
	var b v1.DatabaseBranch
	if err := e.client.Get(context.Background(), types.NamespacedName{Namespace: "shop", Name: name}, &b); err != nil {
		e.t.Fatal(err)
	}
	return &b
}

func TestBranchLatest(t *testing.T) {
	e := readyDB(t)
	markerBefore := e.marker()
	r := e.branch("try", nil)

	b := e.reconcileBranch(r, "try")
	if b.Status.Phase != v1.BranchCreating || b.Status.FromGeneration != 1 || b.Status.Cluster != "db-try" {
		t.Fatalf("status = %+v", b.Status)
	}
	c, ok := e.get(clusterGVK, "db-try")
	if !ok {
		t.Fatal("no Cluster db-try")
	}
	if method, from := bootstrapOf(c); method != "recovery" || from != "db-1" {
		t.Errorf("bootstrap = %s from %q", method, from)
	}
	if targetOf(c) != "" {
		t.Errorf("latest branch has a target %q", targetOf(c))
	}
	if _, has, _ := unstructured.NestedSlice(c.Object, "spec", "plugins"); has {
		t.Error("a branch must not archive WAL (it would write into the database's backups)")
	}
	if n, _, _ := unstructured.NestedInt64(c.Object, "spec", "instances"); n != 1 {
		t.Errorf("instances = %d", n)
	}
	if owner := metav1.GetControllerOf(c); owner == nil || owner.Kind != "DatabaseBranch" {
		t.Errorf("owner = %+v", owner)
	}
	if m := e.marker(); m != markerBefore {
		t.Errorf("the branch changed the database's marker: %+v", m)
	}
	// The database itself is untouched.
	if got := clusterServerName(e.cluster()); got != "db-1" {
		t.Errorf("database Cluster changed: %s", got)
	}

	_ = unstructured.SetNestedSlice(c.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if err := e.client.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if b = e.reconcileBranch(r, "try"); b.Status.Phase != v1.BranchReady {
		t.Errorf("phase = %s", b.Status.Phase)
	}
}

func TestBranchAtATime(t *testing.T) {
	e := readyDB(t)
	at := time.Date(2026, 11, 5, 9, 15, 0, 0, time.UTC)
	r := e.branch("before-migration", &at)
	e.reconcileBranch(r, "before-migration")
	c, ok := e.get(clusterGVK, "db-before-migration")
	if !ok {
		t.Fatal("no Cluster")
	}
	if got := targetOf(c); got != "2026-11-05T09:15:00Z" {
		t.Errorf("target = %q", got)
	}
}

func TestBranchBeforeAnyBackup(t *testing.T) {
	e := readyDB(t)
	at := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	r := e.branch("too-early", &at)
	b := e.reconcileBranch(r, "too-early")
	if b.Status.Phase != v1.BranchFailed {
		t.Errorf("phase = %s", b.Status.Phase)
	}
	if _, ok := e.get(clusterGVK, "db-too-early"); ok {
		t.Error("made a Cluster anyway")
	}
}

func TestBranchWithoutDatabase(t *testing.T) {
	e := newDBEnv(t, tenantNS("shop"))
	r := e.branch("x", nil)
	if b := e.reconcileBranch(r, "x"); b.Status.Phase != v1.BranchFailed {
		t.Errorf("phase = %s", b.Status.Phase)
	}
}
