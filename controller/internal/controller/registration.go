package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/maxemiliang/maxcloud/controller/internal/naming"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// An app's buckets (mc-<app>--*) and database backups (mc-<app>--db) stay
// in Object Storage when its registry entry is deleted. So that whoever
// registers the same name next doesn't get them, every entry carries a
// registration ID, and the controller keeps a record per app name of the
// registration the data belongs to. A Bucket, Database or DatabaseBranch
// is only served when its app's entry is that registration.
const (
	// RegistrationAnnotation on a registry entry is its registration ID:
	// random, given by the controller the first time it sees an entry
	// mcloud made, and kept in the entry's copy so a rebuilt cluster gets
	// it back. An entry from git declares its own (git is an admin's), and
	// the controller never gives one an ID: Flux would race it. Admission
	// lets only the controller and Flux set or change it.
	RegistrationAnnotation = "maxcloud.gronblom.dev/registration"
	// AdoptAnnotation on a registry entry, set by an admin, gives the entry
	// the data of an earlier registration of the same name: its value is
	// that registration's ID (in the blocked message), or "unrecorded" for
	// data from before the controller kept records.
	AdoptAnnotation = "maxcloud.gronblom.dev/adopt-registration"
	// AdoptUnrecorded is AdoptAnnotation's value for data without a record.
	AdoptUnrecorded = "unrecorded"

	registrationPrefix = "registrations/"
	// registrationsSince is when the controller first kept records: data
	// with no record belongs to an entry made before then (an app that was
	// live when records began), never to one made after.
	registrationsSince = registrationPrefix + "_since.json"

	// ReasonRegistration: the app's storage belongs to another
	// registration of its name.
	ReasonRegistration = "RegistrationMismatch"
)

// Registration is the record of which registration of an app name its data
// belongs to, registrations/<app>.json in the registry's bucket. It
// outlives the entry: it's kept as long as any bucket mc-<app>--* exists.
type Registration struct {
	App string `json:"app"`
	ID  string `json:"id"`
	// Since: when this registration took the name's data.
	Since time.Time `json:"since"`
	// Previous registrations whose data this one adopted, oldest first.
	Previous []string `json:"previous,omitempty"`
}

func registrationKey(app string) string { return registrationPrefix + app + ".json" }

// NewRegistrationID returns a new random registration ID.
func NewRegistrationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on Linux
	}
	return hex.EncodeToString(b)
}

// Verdict is whether an app's current entry may use the storage under its
// name.
type Verdict struct {
	// Allowed: go ahead.
	Allowed bool
	// Wait: not yet, for a reason that passes by itself (the entry has no
	// ID yet, or is being deleted). Not worth a status change.
	Wait bool
	// Reason when not allowed: what's wrong and how to fix it.
	Reason string
}

// Registrations decides whether an app's entry is the registration its
// data belongs to (Check), and keeps the records.
type Registrations struct {
	// Client reads the registry's entries.
	Client client.Reader
	// Storage lists the buckets, to tell whether a name still has data.
	Storage objectstorage.Storage
	// Objects and Bucket: the records, in the registry's bucket.
	Objects objectstorage.Objects
	Bucket  string
	// Now is time.Now, replaceable in tests.
	Now func() time.Time

	// Buckets and Databases of the same app check in parallel.
	mu sync.Mutex
}

// Check decides whether the app's registry entry may have the buckets and
// database backups under its name, recording the entry's registration as
// their owner when nobody else's data is there:
//
//   - the record names the entry's ID: yes;
//   - an admin set AdoptAnnotation to the recorded ID: yes, and the record
//     moves to the entry;
//   - no bucket mc-<app>--* exists: yes (a new app, or the old data was
//     deleted), and the record moves to the entry;
//   - data but no record: yes if the entry was made before records began
//     (apps live when this controller version first ran), else no;
//   - data recorded for another ID: no.
//
// A nil Registrations allows everything (tests that aren't about it).
func (r *Registrations) Check(ctx context.Context, app string) (Verdict, error) {
	if r == nil {
		return Verdict{Allowed: true}, nil
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(providerGVK)
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: RegistryNamespace, Name: app}, u); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return Verdict{}, err
		}
		return Verdict{Reason: fmt.Sprintf("app %s has no registry entry (flux-system/%s)", app, app)}, nil
	}
	if !isAppEntry(u) {
		return Verdict{Reason: fmt.Sprintf("flux-system/%s isn't an app's registry entry", app)}, nil
	}
	if !u.GetDeletionTimestamp().IsZero() {
		return Verdict{Wait: true, Reason: "the app's registry entry is being deleted"}, nil
	}
	id := u.GetAnnotations()[RegistrationAnnotation]
	if id == "" {
		if isMcloudEntry(u) {
			return Verdict{Wait: true, Reason: "waiting for the registry entry's registration ID"}, nil
		}
		return Verdict{Reason: fmt.Sprintf(
			"the registry entry %s is from git and has no registration ID: an admin adds the annotation %s "+
				"to it in git (any random value, e.g. from uuidgen; keep it for good, a cluster rebuild needs the same one)",
			app, RegistrationAnnotation)}, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	rec, found, err := r.record(ctx, app)
	if err != nil {
		return Verdict{}, err
	}
	if found && rec.ID == id {
		return Verdict{Allowed: true}, nil
	}
	logger := log.FromContext(ctx).WithValues("app", app, "registration", id)

	adopt := u.GetAnnotations()[AdoptAnnotation]
	if found && adopt == rec.ID || !found && adopt == AdoptUnrecorded {
		prev := []string{}
		if found {
			prev = append(append(prev, rec.Previous...), rec.ID)
		}
		if err := r.put(ctx, app, id, prev); err != nil {
			return Verdict{}, err
		}
		logger.Info("an admin gave the registration an earlier one's data", "adopted", adopt)
		return Verdict{Allowed: true}, nil
	}

	data, err := r.hasData(ctx, app)
	if err != nil {
		return Verdict{}, err
	}
	if !data {
		if err := r.put(ctx, app, id, nil); err != nil {
			return Verdict{}, err
		}
		if found {
			logger.Info("the earlier registration's data is gone; the name's records move to this one", "earlier", rec.ID)
		}
		return Verdict{Allowed: true}, nil
	}

	if !found {
		since, err := r.since(ctx)
		if err != nil {
			return Verdict{}, err
		}
		if u.GetCreationTimestamp().Time.Before(since) {
			if err := r.put(ctx, app, id, nil); err != nil {
				return Verdict{}, err
			}
			logger.Info("app from before registrations were recorded keeps its data")
			return Verdict{Allowed: true}, nil
		}
		return Verdict{Reason: fmt.Sprintf(
			"Object Storage holds data for the name %s (buckets %s*) from before the controller recorded "+
				"registrations, and this registry entry is newer: it doesn't get that data. "+
				"An admin gives it the data with "+
				"`kubectl annotate resourcesetinputprovider -n %s %s %s=%s`, "+
				"or deletes the buckets for a fresh start",
			app, naming.BucketPrefix(app), RegistryNamespace, app, AdoptAnnotation, AdoptUnrecorded)}, nil
	}
	return Verdict{Reason: fmt.Sprintf(
		"the buckets %s* hold the data of an earlier registration of %s (registration %s, since %s), "+
			"and this registry entry is registration %s: it doesn't get that data. "+
			"An admin gives it the data with "+
			"`kubectl annotate resourcesetinputprovider -n %s %s %s=%s`, "+
			"or deletes the buckets for a fresh start",
		naming.BucketPrefix(app), app, rec.ID, rec.Since.UTC().Format(time.RFC3339), id,
		RegistryNamespace, app, AdoptAnnotation, rec.ID)}, nil
}

func (r *Registrations) record(ctx context.Context, app string) (*Registration, bool, error) {
	data, found, err := r.Objects.GetObject(ctx, r.Bucket, registrationKey(app))
	if err != nil || !found {
		return nil, false, err
	}
	var rec Registration
	if err := json.Unmarshal(data, &rec); err != nil || rec.ID == "" {
		// Not one to trust or to overwrite: a person looks at it.
		return nil, false, fmt.Errorf("%s in %s isn't a registration record", registrationKey(app), r.Bucket)
	}
	return &rec, true, nil
}

func (r *Registrations) put(ctx context.Context, app, id string, previous []string) error {
	rec := Registration{App: app, ID: id, Since: r.now().UTC(), Previous: previous}
	if len(rec.Previous) == 0 {
		rec.Previous = nil
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.Objects.PutObject(ctx, r.Bucket, registrationKey(app), data)
}

// hasData reports whether any bucket of the app exists.
func (r *Registrations) hasData(ctx context.Context, app string) (bool, error) {
	buckets, err := r.Storage.Buckets(ctx)
	if err != nil {
		return false, err
	}
	prefix := naming.BucketPrefix(app)
	for b := range buckets {
		if strings.HasPrefix(b, prefix) {
			return true, nil
		}
	}
	return false, nil
}

// since is when records began: written the first time it's asked for,
// which is the first Check of the first controller that keeps records.
func (r *Registrations) since(ctx context.Context) (time.Time, error) {
	var v struct {
		Since time.Time `json:"since"`
	}
	data, found, err := r.Objects.GetObject(ctx, r.Bucket, registrationsSince)
	if err != nil {
		return time.Time{}, err
	}
	if found {
		if err := json.Unmarshal(data, &v); err != nil || v.Since.IsZero() {
			return time.Time{}, fmt.Errorf("%s in %s is unreadable", registrationsSince, r.Bucket)
		}
		return v.Since, nil
	}
	v.Since = r.now().UTC()
	data, _ = json.Marshal(v)
	if err := r.Objects.PutObject(ctx, r.Bucket, registrationsSince, data); err != nil {
		return time.Time{}, err
	}
	log.FromContext(ctx).Info("recording app registrations from now on", "since", v.Since)
	return v.Since, nil
}

// Start writes when records began as soon as the controller leads, so the
// line between apps that were live before and entries made after is the
// rollout, not the first app with leftover data.
func (r *Registrations) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("registrations")
	for {
		err := r.Storage.EnsureBucket(ctx, r.Bucket)
		if err == nil {
			r.mu.Lock()
			_, err = r.since(ctx)
			r.mu.Unlock()
		}
		if err == nil {
			return nil
		}
		logger.Error(err, "recording when registrations began")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

func (r *Registrations) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
