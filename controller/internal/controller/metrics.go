package controller

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

var (
	bucketSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "maxcloud_bucket_size_bytes",
		Help: "Bytes stored in an app's bucket, from UpCloud's bucket metrics.",
	}, []string{"app", "bucket"})
	bucketObjects = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "maxcloud_bucket_objects",
		Help: "Objects in an app's bucket, from UpCloud's bucket metrics.",
	}, []string{"app", "bucket"})
	databasePhase = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "maxcloud_database_phase",
		Help: "1 for an app database's current phase (Creating, Restoring, Ready, Blocked), 0 for the others.",
	}, []string{"app", "phase"})
	databaseGeneration = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "maxcloud_database_generation",
		Help: "The backup generation an app database writes to; it goes up with every restore.",
	}, []string{"app"})
	tokenExpiry = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "maxcloud_upcloud_token_expiry_timestamp_seconds",
		Help: "When the controller's UpCloud API token expires (Unix time).",
	})
)

func init() {
	metrics.Registry.MustRegister(bucketSize, bucketObjects, tokenExpiry, databasePhase, databaseGeneration)
}

// SetTokenExpiry publishes when the UpCloud token runs out, for an alert.
func SetTokenExpiry(t time.Time) { tokenExpiry.Set(float64(t.Unix())) }

// MetricsCache holds UpCloud's bucket metrics for a while, so reconciling
// every bucket doesn't list all buckets once per bucket.
type MetricsCache struct {
	Storage objectstorage.Storage
	MaxAge  time.Duration

	mu      sync.Mutex
	fetched time.Time
	buckets map[string]objectstorage.BucketMetrics
}

// Get returns the metrics of one bucket, and whether UpCloud knows it.
func (c *MetricsCache) Get(ctx context.Context, bucket string) (objectstorage.BucketMetrics, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buckets == nil || time.Since(c.fetched) > c.MaxAge {
		b, err := c.Storage.Buckets(ctx)
		if err != nil {
			return objectstorage.BucketMetrics{}, false, err
		}
		c.buckets, c.fetched = b, time.Now()
	}
	m, ok := c.buckets[bucket]
	return m, ok, nil
}

func setDatabaseMetrics(app string, phase v1.DatabasePhase, generation int64) {
	for _, p := range []v1.DatabasePhase{v1.PhaseCreating, v1.PhaseRestoring, v1.PhaseReady, v1.PhaseBlocked} {
		v := 0.0
		if p == phase {
			v = 1
		}
		databasePhase.WithLabelValues(app, string(p)).Set(v)
	}
	if generation > 0 {
		databaseGeneration.WithLabelValues(app).Set(float64(generation))
	}
}

func deleteDatabaseMetrics(app string) {
	for _, p := range []v1.DatabasePhase{v1.PhaseCreating, v1.PhaseRestoring, v1.PhaseReady, v1.PhaseBlocked} {
		databasePhase.DeleteLabelValues(app, string(p))
	}
	databaseGeneration.DeleteLabelValues(app)
}
