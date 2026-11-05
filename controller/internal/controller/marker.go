package controller

import (
	"encoding/json"
	"fmt"
	"time"
)

// Marker is the object maxcloud.json in an app's database bucket: which
// backup generation is current. It lives in the bucket, not in the cluster,
// so it survives losing the namespace or the whole cluster; the app's
// policy can't write it.
//
// Generation N's backups go to the folder db-N. A restore always starts a
// new generation, so older backups are never written over.
type Marker struct {
	Generation int64 `json:"generation"`
	// State of the current generation:
	//   initializing  a new, empty database (initdb), no base backup yet
	//   restoring     restored from RestoredFrom, no base backup of its own yet
	//   ready         has a base backup of its own
	State        string `json:"state"`
	RestoredFrom int64  `json:"restoredFrom,omitempty"`
	// TargetTime: the restore stops at this point in time (restore-to),
	// not at the end of RestoredFrom's WAL.
	TargetTime *time.Time `json:"targetTime,omitempty"`
	Updated    time.Time  `json:"updated"`
}

const (
	StateInitializing = "initializing"
	StateRestoring    = "restoring"
	StateReady        = "ready"
)

func parseMarker(data []byte) (*Marker, error) {
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("marker isn't JSON: %w", err)
	}
	if m.Generation < 1 {
		return nil, fmt.Errorf("marker has generation %d", m.Generation)
	}
	switch m.State {
	case StateInitializing, StateReady:
	case StateRestoring:
		if m.RestoredFrom < 1 {
			return nil, fmt.Errorf("marker says restoring but not from what")
		}
	default:
		return nil, fmt.Errorf("marker has state %q", m.State)
	}
	return &m, nil
}

func (m Marker) json() []byte {
	b, _ := json.Marshal(m)
	return b
}

// serverName is generation n's folder in the bucket (barman's server name).
func serverName(n int64) string { return fmt.Sprintf("db-%d", n) }

// bucketView is what the controller found in the bucket.
type bucketView struct {
	// Exists: the bucket is there. Empty: it holds no objects at all.
	Exists, Empty bool
	// Marker, nil if there's none.
	Marker *Marker
	// Base[n]: generation n has a finished base backup. Any[n]: it has any
	// object at all (WAL counts). Only filled for the generations decide
	// asks about.
	Base, Any map[int64]bool
}

type action int

const (
	actInit    action = iota // a new, empty database: initdb
	actRestore               // restore From into a new Generation
	actBlock                 // don't guess: a person decides
)

// plan is what to do when the database's Cluster doesn't exist.
type plan struct {
	Action     action
	Generation int64      // the generation the new Cluster writes to
	From       int64      // actRestore: the generation to restore
	Target     *time.Time // actRestore: stop at this time, nil for the latest
	Reason     string
}

// generationsToCheck lists the generations decide needs Base and Any for.
func generationsToCheck(m *Marker, recoverFrom int64) []int64 {
	var gens []int64
	if m != nil {
		gens = append(gens, m.Generation)
		if m.RestoredFrom > 0 {
			gens = append(gens, m.RestoredFrom)
		}
	}
	if recoverFrom > 0 {
		gens = append(gens, recoverFrom)
	}
	return gens
}

// decide what to do about a database whose Cluster is missing: on a new
// app, after the namespace or the whole cluster was lost, or after someone
// deleted the Cluster. It never starts an empty database where there could
// be data to restore.
//
// recoverFrom is a generation a person asked for (RecoverFromAnnotation),
// 0 if none.
func decide(v bucketView, recoverFrom int64) plan {
	next := func() int64 {
		if v.Marker != nil {
			return max(v.Marker.Generation, recoverFrom) + 1
		}
		return recoverFrom + 1
	}

	if recoverFrom > 0 {
		if !v.Base[recoverFrom] {
			return plan{Action: actBlock, Reason: fmt.Sprintf(
				"asked to recover from generation %d, which has no finished base backup", recoverFrom)}
		}
		return plan{Action: actRestore, From: recoverFrom, Generation: next(),
			Reason: fmt.Sprintf("restoring generation %d, as asked", recoverFrom)}
	}

	if !v.Exists {
		return plan{Action: actInit, Generation: 1, Reason: "new database"}
	}
	m := v.Marker
	if m == nil {
		if v.Empty {
			return plan{Action: actInit, Generation: 1, Reason: "new database (empty bucket)"}
		}
		return plan{Action: actBlock, Reason: "the backup bucket has objects but no " + markerKey +
			": not starting an empty database over them"}
	}

	n := m.Generation
	switch m.State {
	case StateReady:
		if v.Base[n] {
			return plan{Action: actRestore, From: n, Generation: n + 1,
				Reason: fmt.Sprintf("the Cluster is gone: restoring generation %d", n)}
		}
		return plan{Action: actBlock, Reason: fmt.Sprintf(
			"generation %d is marked ready but has no finished base backup", n)}

	case StateInitializing:
		if !v.Any[n] {
			// Nothing was ever archived: nothing to lose.
			return plan{Action: actInit, Generation: n, Reason: "new database (the first try didn't get far)"}
		}
		if v.Base[n] {
			return plan{Action: actRestore, From: n, Generation: n + 1,
				Reason: fmt.Sprintf("the Cluster is gone: restoring generation %d", n)}
		}
		return plan{Action: actBlock, Reason: fmt.Sprintf(
			"generation %d has archived WAL but no base backup to restore it onto", n)}

	case StateRestoring:
		from := m.RestoredFrom
		switch {
		case v.Base[n]:
			return plan{Action: actRestore, From: n, Generation: n + 1,
				Reason: fmt.Sprintf("the Cluster is gone: restoring generation %d", n)}
		case !v.Any[n] && v.Base[from]:
			// The restore never got to archive anything (or is a restore to
			// a point in time that was just planned): do it.
			return plan{Action: actRestore, From: from, Generation: n, Target: m.TargetTime,
				Reason: restoreReason(from, m.TargetTime)}
		case v.Base[from]:
			// It archived some WAL but no base backup: leave that folder
			// alone and restore the source again into a new one.
			return plan{Action: actRestore, From: from, Generation: n + 1, Target: m.TargetTime,
				Reason: restoreReason(from, m.TargetTime) + " (into a new generation)"}
		}
		return plan{Action: actBlock, Reason: fmt.Sprintf(
			"generation %d was being restored from %d, and neither has a finished base backup", n, from)}
	}
	return plan{Action: actBlock, Reason: "unknown marker state " + m.State}
}

// markerKey is the marker's key in the bucket.
const markerKey = "maxcloud.json"

func restoreReason(from int64, target *time.Time) string {
	if target != nil {
		return fmt.Sprintf("restoring generation %d to %s", from, target.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("restoring generation %d", from)
}
