// maxcloud-controller gives apps on the run platform the storage the app
// registry asks for: S3 buckets in UpCloud Managed Object Storage, and
// Postgres databases (CloudNativePG) backed up to them.
//
// Configuration, from the environment:
//
//	UPCLOUD_TOKEN                API token of a sub-account scoped to the instance
//	UPCLOUD_TOKEN_EXPIRES_AT     optional, RFC 3339: published as a metric
//	MAXCLOUD_OBJECT_STORAGE_ID   the Object Storage instance's UUID
//	MAXCLOUD_S3_ENDPOINT         its S3 endpoint, https://...
//	MAXCLOUD_S3_REGION           default europe-1
//	MAXCLOUD_POSTGRES_IMAGE      the databases' Postgres image (default in the code)
//	MAXCLOUD_DB_STORAGE_CLASS    the databases' StorageClass (default: the cluster's)
//	MAXCLOUD_PUBLIC_DOMAIN       gives public buckets <bucket>--<app>.<domain> (off if unset)
//	MAXCLOUD_PUBLIC_GATEWAY      namespace/gateway/listener for those (default traefik/public/https-public)
//	POD_NAMESPACE                where their route objects go (default maxcloud-system)
//	AWS_ACCESS_KEY_ID            the controller's own S3 user
//	AWS_SECRET_ACCESS_KEY
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/controller"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// Version is set at build time.
var Version = "dev"

type config struct {
	token, serviceUUID, endpoint, region, accessKeyID, secretAccessKey string
	postgresImage, dbStorageClass                                      string
	publicDomain, publicGateway, namespace                             string
	tokenExpires                                                       time.Time
}

func configFromEnv() (config, error) {
	c := config{
		token:           os.Getenv("UPCLOUD_TOKEN"),
		serviceUUID:     os.Getenv("MAXCLOUD_OBJECT_STORAGE_ID"),
		endpoint:        strings.TrimRight(os.Getenv("MAXCLOUD_S3_ENDPOINT"), "/"),
		region:          os.Getenv("MAXCLOUD_S3_REGION"),
		accessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		secretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
	}
	if c.region == "" {
		c.region = "europe-1"
	}
	c.dbStorageClass = os.Getenv("MAXCLOUD_DB_STORAGE_CLASS")
	c.publicDomain = os.Getenv("MAXCLOUD_PUBLIC_DOMAIN")
	if c.publicGateway = os.Getenv("MAXCLOUD_PUBLIC_GATEWAY"); c.publicGateway == "" {
		c.publicGateway = "traefik/public/https-public"
	}
	if c.namespace = os.Getenv("POD_NAMESPACE"); c.namespace == "" {
		c.namespace = "maxcloud-system"
	}
	if c.postgresImage = os.Getenv("MAXCLOUD_POSTGRES_IMAGE"); c.postgresImage == "" {
		c.postgresImage = controller.DefaultPostgresImage
	}
	var missing []string
	for name, v := range map[string]string{
		"UPCLOUD_TOKEN":              c.token,
		"MAXCLOUD_OBJECT_STORAGE_ID": c.serviceUUID,
		"MAXCLOUD_S3_ENDPOINT":       c.endpoint,
		"AWS_ACCESS_KEY_ID":          c.accessKeyID,
		"AWS_SECRET_ACCESS_KEY":      c.secretAccessKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	if !strings.HasPrefix(c.endpoint, "https://") {
		return c, errors.New("MAXCLOUD_S3_ENDPOINT must start with https://")
	}
	if s := os.Getenv("UPCLOUD_TOKEN_EXPIRES_AT"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return c, fmt.Errorf("UPCLOUD_TOKEN_EXPIRES_AT: %w", err)
		}
		c.tokenExpires = t
	}
	return c, nil
}

func main() {
	var metricsAddr, probeAddr string
	var leaderElect bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address of the Prometheus metrics endpoint (plain HTTP).")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address of the health probes.")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Use a Lease so only one controller acts during a rollout.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	if err := run(metricsAddr, probeAddr, leaderElect); err != nil {
		log.Error(err, "exiting")
		os.Exit(1)
	}
}

func run(metricsAddr, probeAddr string, leaderElect bool) error {
	cfg, err := configFromEnv()
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "maxcloud-controller",
	})
	if err != nil {
		return fmt.Errorf("manager: %w", err)
	}

	storage := objectstorage.NewUpCloud(cfg.token, cfg.serviceUUID)
	s3c := objectstorage.NewS3(cfg.endpoint, cfg.region, cfg.accessKeyID, cfg.secretAccessKey)
	identity := &controller.Identity{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Storage:   storage,
		Endpoint:  cfg.endpoint,
		Region:    cfg.region,
	}
	var routes *controller.PublicRoutes
	if cfg.publicDomain != "" {
		gw := strings.Split(cfg.publicGateway, "/")
		if len(gw) != 3 {
			return fmt.Errorf("MAXCLOUD_PUBLIC_GATEWAY %q: expected namespace/gateway/listener", cfg.publicGateway)
		}
		u, err := url.Parse(cfg.endpoint)
		if err != nil {
			return fmt.Errorf("MAXCLOUD_S3_ENDPOINT: %w", err)
		}
		routes = &controller.PublicRoutes{
			Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Namespace: cfg.namespace,
			Domain: cfg.publicDomain, GatewayNamespace: gw[0], Gateway: gw[1], Listener: gw[2],
			S3Host: u.Hostname(),
		}
	}
	if err := (&controller.BucketReconciler{
		Client:   mgr.GetClient(),
		Identity: identity,
		Storage:  storage,
		Policies: s3c,
		Metrics:  &controller.MetricsCache{Storage: storage, MaxAge: 2 * time.Minute},
		Routes:   routes,
		Endpoint: cfg.endpoint,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("bucket controller: %w", err)
	}
	databases := &controller.DatabaseReconciler{
		Client:       mgr.GetClient(),
		Identity:     identity,
		Storage:      storage,
		Objects:      s3c,
		Endpoint:     cfg.endpoint,
		Image:        cfg.postgresImage,
		StorageClass: cfg.dbStorageClass,
	}
	if err := databases.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("database controller: %w", err)
	}
	if err := (&controller.BranchReconciler{Client: mgr.GetClient(), DB: databases}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("database branch controller: %w", err)
	}

	if !cfg.tokenExpires.IsZero() {
		controller.SetTokenExpiry(cfg.tokenExpires)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	ctrl.Log.WithName("setup").Info("starting", "version", Version, "objectStorage", cfg.serviceUUID, "endpoint", cfg.endpoint)
	return mgr.Start(ctrl.SetupSignalHandler())
}
