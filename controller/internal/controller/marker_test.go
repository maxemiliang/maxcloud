package controller

import (
	"strings"
	"testing"

	"github.com/maxemiliang/maxcloud/controller/internal/s3policy"
)

func TestMarkerKeyIsTheProtectedOne(t *testing.T) {
	// The app's policy denies writing this key; they must agree.
	if markerKey != s3policy.MarkerKey {
		t.Fatalf("markerKey %q != s3policy.MarkerKey %q", markerKey, s3policy.MarkerKey)
	}
}

func TestParseMarker(t *testing.T) {
	m, err := parseMarker([]byte(`{"generation":3,"state":"restoring","restoredFrom":2}`))
	if err != nil || m.Generation != 3 || m.RestoredFrom != 2 {
		t.Fatalf("parseMarker = %+v, %v", m, err)
	}
	for _, bad := range []string{`nope`, `{"generation":0,"state":"ready"}`, `{"generation":1,"state":"odd"}`, `{"generation":2,"state":"restoring"}`} {
		if _, err := parseMarker([]byte(bad)); err == nil {
			t.Errorf("parseMarker(%s): want an error", bad)
		}
	}
	back, err := parseMarker(Marker{Generation: 2, State: StateReady}.json())
	if err != nil || back.Generation != 2 {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

func TestDecide(t *testing.T) {
	marker := func(gen int64, state string, from int64) *Marker {
		return &Marker{Generation: gen, State: state, RestoredFrom: from}
	}
	gens := func(ns ...int64) map[int64]bool {
		m := map[int64]bool{}
		for _, n := range ns {
			m[n] = true
		}
		return m
	}
	for _, tc := range []struct {
		name        string
		view        bucketView
		recoverFrom int64
		want        plan
	}{
		{
			name: "no bucket: a new database",
			view: bucketView{},
			want: plan{Action: actInit, Generation: 1},
		},
		{
			name: "empty bucket, no marker: a new database",
			view: bucketView{Exists: true, Empty: true},
			want: plan{Action: actInit, Generation: 1},
		},
		{
			name: "objects but no marker: blocked",
			view: bucketView{Exists: true},
			want: plan{Action: actBlock},
		},
		{
			name: "ready generation with a backup: restore into the next",
			view: bucketView{Exists: true, Marker: marker(3, StateReady, 0), Base: gens(3), Any: gens(3)},
			want: plan{Action: actRestore, From: 3, Generation: 4},
		},
		{
			name: "ready but no backup: blocked",
			view: bucketView{Exists: true, Marker: marker(3, StateReady, 0), Any: gens(3)},
			want: plan{Action: actBlock},
		},
		{
			name: "initializing, nothing archived: start over in the same generation",
			view: bucketView{Exists: true, Marker: marker(1, StateInitializing, 0)},
			want: plan{Action: actInit, Generation: 1},
		},
		{
			name: "initializing, but already backed up: restore it",
			view: bucketView{Exists: true, Marker: marker(1, StateInitializing, 0), Base: gens(1), Any: gens(1)},
			want: plan{Action: actRestore, From: 1, Generation: 2},
		},
		{
			name: "initializing, WAL but no base backup: blocked, never initdb over WAL",
			view: bucketView{Exists: true, Marker: marker(1, StateInitializing, 0), Any: gens(1)},
			want: plan{Action: actBlock},
		},
		{
			name: "restoring, the new generation got its backup: restore that",
			view: bucketView{Exists: true, Marker: marker(4, StateRestoring, 3), Base: gens(3, 4), Any: gens(3, 4)},
			want: plan{Action: actRestore, From: 4, Generation: 5},
		},
		{
			name: "restoring, nothing archived yet: retry into the same generation",
			view: bucketView{Exists: true, Marker: marker(4, StateRestoring, 3), Base: gens(3), Any: gens(3)},
			want: plan{Action: actRestore, From: 3, Generation: 4},
		},
		{
			name: "restoring, some WAL archived: retry into a new generation",
			view: bucketView{Exists: true, Marker: marker(4, StateRestoring, 3), Base: gens(3), Any: gens(3, 4)},
			want: plan{Action: actRestore, From: 3, Generation: 5},
		},
		{
			name: "restoring, the source has no backup either: blocked",
			view: bucketView{Exists: true, Marker: marker(4, StateRestoring, 3), Any: gens(3)},
			want: plan{Action: actBlock},
		},
		{
			name:        "a person asks for generation 2: restore it into a new generation",
			view:        bucketView{Exists: true, Marker: marker(4, StateReady, 0), Base: gens(2, 4), Any: gens(2, 4)},
			recoverFrom: 2,
			want:        plan{Action: actRestore, From: 2, Generation: 5},
		},
		{
			name:        "a person asks for a generation without a backup: blocked",
			view:        bucketView{Exists: true, Marker: marker(4, StateReady, 0), Base: gens(4), Any: gens(4)},
			recoverFrom: 2,
			want:        plan{Action: actBlock},
		},
		{
			name:        "recover from a generation when the marker is lost",
			view:        bucketView{Exists: true, Base: gens(2), Any: gens(2)},
			recoverFrom: 2,
			want:        plan{Action: actRestore, From: 2, Generation: 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.view, tc.recoverFrom)
			if got.Action != tc.want.Action || got.Generation != tc.want.Generation || got.From != tc.want.From {
				t.Errorf("decide = %+v, want %+v", got, tc.want)
			}
			if got.Reason == "" {
				t.Error("no reason given")
			}
		})
	}
}

// Whatever the bucket holds, decide never plans initdb when there's
// archived data it could lose.
func TestDecideNeverInitsOverData(t *testing.T) {
	states := []string{StateInitializing, StateRestoring, StateReady}
	for _, st := range states {
		for _, base := range []bool{false, true} {
			v := bucketView{Exists: true, Marker: &Marker{Generation: 2, State: st, RestoredFrom: 1},
				Base: map[int64]bool{2: base, 1: base}, Any: map[int64]bool{2: true, 1: true}}
			if p := decide(v, 0); p.Action == actInit {
				t.Errorf("state %s, base %v, WAL archived: decide = initdb (%s)", st, base, p.Reason)
			}
		}
	}
	if p := decide(bucketView{Exists: true, Empty: false}, 0); p.Action == actInit || !strings.Contains(p.Reason, "no maxcloud.json") {
		t.Errorf("objects without marker: %+v", p)
	}
}
