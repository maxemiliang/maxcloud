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
//	MAXCLOUD_DOMAINS_GATEWAY     namespace/gateway apps' custom domains attach to (default traefik/public)
//	MAXCLOUD_DOMAINS_ALLOWLIST   ConfigMap in POD_NAMESPACE: the hostnames each app may have (default maxcloud-domains)
//	MAXCLOUD_REGISTRY_BUCKET     where the app registry's in-cluster entries are copied (default maxcloud-registry)
//	MAXCLOUD_SECRETS_AGE_KEY     age identity (AGE-SECRET-KEY-1...): turns on the apps' secrets backup (off if unset)
//	MAXCLOUD_IMAGE_REGISTRY      the maxcloud registry's host: apps get pull credentials for it (off if unset)
//	MAXCLOUD_API_SERVER          the API server's external URL: turns on the discovery document (off if unset)
//	MAXCLOUD_DISCOVERY_ADDR      where that's served (default :8082)
//	MAXCLOUD_OIDC_ISSUER         optional, in the discovery document
//	MAXCLOUD_OIDC_CLIENT_ID      optional, in the discovery document
//	MAXCLOUD_MIN_MCLOUD_VERSION  optional, in the discovery document
//	MAXCLOUD_GATEWAY_URL         the API gateway's public URL: turns it on, and puts it in the discovery document (off if unset; needs the two OIDC settings)
//	MAXCLOUD_GATEWAY_ADDR        where the gateway listens (default :8083)
//	MAXCLOUD_GATEWAY_USER_PREFIX what the API server puts before a person's username (default authelia:)
//	MAXCLOUD_LOKI_URL            Loki's base URL: turns on apps' logs (/api/v1/apps/ on the API gateway, so it needs MAXCLOUD_GATEWAY_URL)
//	MAXCLOUD_PROMETHEUS_URL      Prometheus's base URL: turns on apps' metrics and alerts (likewise)
//	MAXCLOUD_ALERT_RULE_NAMESPACES  namespaces whose PrometheusRules' alerts apps see (default monitoring)
//	AWS_ACCESS_KEY_ID            the controller's own S3 user
//	AWS_SECRET_ACCESS_KEY
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "github.com/maxemiliang/maxcloud/controller/api/v1alpha1"
	"github.com/maxemiliang/maxcloud/controller/internal/appapi"
	"github.com/maxemiliang/maxcloud/controller/internal/caller"
	"github.com/maxemiliang/maxcloud/controller/internal/controller"
	"github.com/maxemiliang/maxcloud/controller/internal/discovery"
	"github.com/maxemiliang/maxcloud/controller/internal/gateway"
	"github.com/maxemiliang/maxcloud/controller/internal/objectstorage"
)

// Version is set at build time.
var Version = "dev"

type config struct {
	token, serviceUUID, endpoint, region, accessKeyID, secretAccessKey string
	postgresImage, dbStorageClass                                      string
	publicDomain, publicGateway, namespace                             string
	traefikSafeNaming                                                  bool
	domainsGateway, domainsAllowlist                                   string
	registryBucket                                                     string
	secretsKey                                                         *age.X25519Identity
	apiServer, discoveryAddr, oidcIssuer, oidcClientID, minMcloud      string
	gatewayURL, gatewayAddr, gatewayUserPrefix                         string
	imageRegistry, zotNamespace                                        string
	lokiURL, prometheusURL                                             string
	alertRuleNamespaces                                                []string
	pullTokenLifetime                                                  time.Duration
	tokenExpires                                                       time.Time
	maxEnvironments, maxPreviews                                       int
	previewQuota                                                       controller.PreviewQuota
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
	if c.registryBucket = os.Getenv("MAXCLOUD_REGISTRY_BUCKET"); c.registryBucket == "" {
		c.registryBucket = "maxcloud-registry"
	}
	if s := os.Getenv("MAXCLOUD_SECRETS_AGE_KEY"); s != "" {
		id, err := controller.ParseAgeIdentity(s)
		if err != nil {
			// Not the error itself: it could quote the key.
			return c, errors.New("MAXCLOUD_SECRETS_AGE_KEY: not an age X25519 identity (AGE-SECRET-KEY-1...)")
		}
		c.secretsKey = id
	}
	c.apiServer = strings.TrimRight(os.Getenv("MAXCLOUD_API_SERVER"), "/")
	if c.discoveryAddr = os.Getenv("MAXCLOUD_DISCOVERY_ADDR"); c.discoveryAddr == "" {
		c.discoveryAddr = ":8082"
	}
	c.oidcIssuer = os.Getenv("MAXCLOUD_OIDC_ISSUER")
	c.oidcClientID = os.Getenv("MAXCLOUD_OIDC_CLIENT_ID")
	c.minMcloud = os.Getenv("MAXCLOUD_MIN_MCLOUD_VERSION")
	c.gatewayURL = strings.TrimRight(os.Getenv("MAXCLOUD_GATEWAY_URL"), "/")
	if c.gatewayAddr = os.Getenv("MAXCLOUD_GATEWAY_ADDR"); c.gatewayAddr == "" {
		c.gatewayAddr = ":8083"
	}
	if c.gatewayUserPrefix = os.Getenv("MAXCLOUD_GATEWAY_USER_PREFIX"); c.gatewayUserPrefix == "" {
		c.gatewayUserPrefix = "authelia:"
	}
	if c.gatewayURL != "" && (c.oidcIssuer == "" || c.oidcClientID == "") {
		return c, errors.New("MAXCLOUD_GATEWAY_URL needs MAXCLOUD_OIDC_ISSUER and MAXCLOUD_OIDC_CLIENT_ID: the gateway only passes on that provider's logins")
	}
	c.imageRegistry = os.Getenv("MAXCLOUD_IMAGE_REGISTRY")
	c.lokiURL = strings.TrimRight(os.Getenv("MAXCLOUD_LOKI_URL"), "/")
	c.prometheusURL = strings.TrimRight(os.Getenv("MAXCLOUD_PROMETHEUS_URL"), "/")
	for _, ns := range strings.Split(os.Getenv("MAXCLOUD_ALERT_RULE_NAMESPACES"), ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			c.alertRuleNamespaces = append(c.alertRuleNamespaces, ns)
		}
	}
	c.zotNamespace = os.Getenv("MAXCLOUD_ZOT_NAMESPACE")
	c.pullTokenLifetime = 7 * 24 * time.Hour
	if s := os.Getenv("MAXCLOUD_IMAGE_PULL_TOKEN_LIFETIME"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 10*time.Minute {
			return c, fmt.Errorf("MAXCLOUD_IMAGE_PULL_TOKEN_LIFETIME %q: a duration of at least 10m", s)
		}
		c.pullTokenLifetime = d
	}
	for _, v := range []struct {
		name string
		dst  *int
		def  int
	}{{"MAXCLOUD_ENVIRONMENTS_MAX", &c.maxEnvironments, 3}, {"MAXCLOUD_PREVIEWS_MAX", &c.maxPreviews, 6}} {
		*v.dst = v.def
		if s := os.Getenv(v.name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return c, fmt.Errorf("%s %q: a whole number", v.name, s)
			}
			*v.dst = n
		}
	}
	c.previewQuota = controller.DefaultPreviewQuota
	if s := os.Getenv("MAXCLOUD_PREVIEW_QUOTA"); s != "" {
		// cpu,memory,pods,dbSize: 100m,512Mi,10,1Gi
		parts := strings.Split(s, ",")
		if len(parts) != 4 {
			return c, fmt.Errorf("MAXCLOUD_PREVIEW_QUOTA %q: cpu,memory,pods,dbSize (100m,512Mi,10,1Gi)", s)
		}
		c.previewQuota = controller.PreviewQuota{CPU: parts[0], Memory: parts[1], Pods: parts[2], DBSize: parts[3]}
	}
	c.publicDomain = os.Getenv("MAXCLOUD_PUBLIC_DOMAIN")
	if c.publicGateway = os.Getenv("MAXCLOUD_PUBLIC_GATEWAY"); c.publicGateway == "" {
		c.publicGateway = "traefik/public/https-public"
	}
	if s := os.Getenv("MAXCLOUD_TRAEFIK_SAFE_NAMING"); s != "" {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return c, fmt.Errorf("MAXCLOUD_TRAEFIK_SAFE_NAMING %q: true or false", s)
		}
		c.traefikSafeNaming = v
	}
	if c.namespace = os.Getenv("POD_NAMESPACE"); c.namespace == "" {
		c.namespace = "maxcloud-system"
	}
	if c.domainsGateway = os.Getenv("MAXCLOUD_DOMAINS_GATEWAY"); c.domainsGateway == "" {
		c.domainsGateway = "traefik/public"
	}
	if c.domainsAllowlist = os.Getenv("MAXCLOUD_DOMAINS_ALLOWLIST"); c.domainsAllowlist == "" {
		c.domainsAllowlist = "maxcloud-domains"
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

	restConfig := ctrl.GetConfigOrDie()
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "maxcloud-controller",
		// The only Secrets and ConfigMaps read through the cache are apps'
		// app-secrets and app-env (mcloud's); the rest are read uncached, so
		// not every one in the cluster is held in memory.
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&corev1.Secret{}:    {Label: labels.SelectorFromSet(labels.Set{controller.ManagedByLabel: "mcloud"})},
			&corev1.ConfigMap{}: {Label: labels.SelectorFromSet(labels.Set{controller.ManagedByLabel: "mcloud"})},
		}},
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
			S3Host: u.Hostname(), SafeNaming: cfg.traefikSafeNaming,
		}
	}
	// Which registration of an app name its buckets and backups belong
	// to: a new entry under an old name doesn't get the old app's data.
	registrations := &controller.Registrations{
		Client: mgr.GetClient(), Storage: storage, Objects: s3c, Bucket: cfg.registryBucket,
	}
	if err := mgr.Add(registrations); err != nil {
		return fmt.Errorf("registrations: %w", err)
	}
	if err := (&controller.BucketReconciler{
		Client:   mgr.GetClient(),
		Identity: identity,
		Storage:  storage,
		Policies: s3c,
		Metrics:  &controller.MetricsCache{Storage: storage, MaxAge: 2 * time.Minute},
		Routes:   routes,
		Endpoint: cfg.endpoint,

		Registrations: registrations,
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

		Registrations: registrations,
	}
	if err := databases.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("database controller: %w", err)
	}
	if err := (&controller.BranchReconciler{Client: mgr.GetClient(), DB: databases}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("database branch controller: %w", err)
	}

	// Apps' custom hostnames (Domains, from the registry's domains field).
	gw := strings.Split(cfg.domainsGateway, "/")
	if len(gw) != 2 {
		return fmt.Errorf("MAXCLOUD_DOMAINS_GATEWAY %q: expected namespace/gateway", cfg.domainsGateway)
	}
	if err := (&controller.Domains{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(),
		AllowlistNamespace: cfg.namespace, Allowlist: cfg.domainsAllowlist,
		GatewayNamespace: gw[0], Gateway: gw[1],
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("domain controller: %w", err)
	}

	// Apps' secrets live only in the cluster: an encrypted copy of each goes
	// to the registry's bucket, restored after the registry's entries.
	var secrets *controller.SecretBackup
	if cfg.secretsKey != nil {
		secrets = &controller.SecretBackup{
			Client: mgr.GetClient(), Storage: storage, Objects: s3c, Bucket: cfg.registryBucket, Identity: cfg.secretsKey,
		}
		if err := secrets.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("app secrets backup: %w", err)
		}
	} else {
		ctrl.Log.WithName("setup").Info("MAXCLOUD_SECRETS_AGE_KEY unset: apps' secrets aren't backed up")
	}
	// Their plain variables (mcloud env set) too, as JSON: no key needed.
	env := &controller.EnvBackup{Client: mgr.GetClient(), Storage: storage, Objects: s3c, Bucket: cfg.registryBucket}
	if err := env.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("app env backup: %w", err)
	}
	if err := (&controller.RegistryBackup{
		Client: mgr.GetClient(), Storage: storage, Objects: s3c, Bucket: cfg.registryBucket, Secrets: secrets, Env: env,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registry backup: %w", err)
	}

	// Apps' logs, metrics and alerts, for their owners: served on the API
	// gateway, so a request gets here only with a login that passed its
	// check; the handler then asks who it is and what they may do in the
	// app's namespace (TokenReview, SubjectAccessReview), and fixes the
	// namespace in every query itself.
	var appsAPI *appapi.Handler
	switch {
	case cfg.lokiURL == "" && cfg.prometheusURL == "":
	case cfg.gatewayURL == "":
		ctrl.Log.WithName("setup").Info("MAXCLOUD_LOKI_URL/MAXCLOUD_PROMETHEUS_URL need MAXCLOUD_GATEWAY_URL (the API gateway they're served on): apps' logs, metrics and alerts are off")
	default:
		appsAPI = &appapi.Handler{
			Reviewer: &caller.Reviewer{Client: mgr.GetClient()},
			Reader:   mgr.GetAPIReader(),
			Loki:     cfg.lokiURL, Prometheus: cfg.prometheusURL,
			RuleNamespaces: cfg.alertRuleNamespaces,
		}
		appsAPI.Init()
	}

	// The public discovery document, on every replica.
	if cfg.apiServer != "" {
		doc := discovery.Document{APIServer: cfg.apiServer, Gateway: cfg.gatewayURL, ImageRegistry: cfg.imageRegistry}
		if cfg.oidcIssuer != "" {
			doc.OIDC = &discovery.OIDC{Issuer: cfg.oidcIssuer, ClientID: cfg.oidcClientID}
		}
		if cfg.minMcloud != "" {
			doc.Mcloud = &discovery.Mcloud{MinVersion: cfg.minMcloud}
		}
		// Apps' logs, metrics and alerts are served on the API gateway;
		// the document says where.
		if appsAPI != nil {
			doc.API = &discovery.API{Apps: cfg.gatewayURL + appapi.Prefix, Features: appsAPI.Features()}
		}
		if err := mgr.Add(&discovery.Server{
			Addr: cfg.discoveryAddr, Reader: mgr.GetAPIReader(), Namespace: cfg.namespace, Document: doc,
		}); err != nil {
			return fmt.Errorf("discovery: %w", err)
		}
	}

	// The API gateway, on every replica: the API server for people whose
	// address isn't on its allowlist, with their own login.
	if cfg.gatewayURL != "" {
		gw, err := newGateway(restConfig, cfg)
		if err != nil {
			return fmt.Errorf("gateway: %w", err)
		}
		if appsAPI != nil {
			gw.Mounts = map[string]http.Handler{appapi.Prefix: appsAPI}
		}
		if err := mgr.Add(gw); err != nil {
			return fmt.Errorf("gateway: %w", err)
		}
	}

	// Apps pull their images from the maxcloud registry with tokens of their
	// own ServiceAccount maxcloud-image-pull.
	if cfg.imageRegistry != "" {
		if err := (&controller.ImagePull{
			Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(),
			Host: cfg.imageRegistry, Lifetime: cfg.pullTokenLifetime,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("image pull: %w", err)
		}
	}
	// The registry's access rules: one per app, from the app registry.
	if cfg.zotNamespace != "" {
		if err := (&controller.ZotAccess{
			Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Namespace: cfg.zotNamespace,
		}).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("registry access: %w", err)
		}
	}

	// GitOps apps' maxcloud.yaml reaches their registry entry through Flux.
	// Writes impersonate maxcloud:app-sync, so the admission ceiling applies
	// as for a developer.
	syncConfig := rest.CopyConfig(restConfig)
	syncConfig.Impersonate = rest.ImpersonationConfig{UserName: "maxcloud:app-sync"}
	syncWriter, err := client.New(syncConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("app sync client: %w", err)
	}
	if err := (&controller.AppSync{
		Client: mgr.GetClient(), Writer: syncWriter, Recorder: mgr.GetEventRecorderFor("maxcloud-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("app sync: %w", err)
	}

	// Apps' environments and previews: registry entries of their own,
	// written as maxcloud:environments, so the developer ceiling applies to
	// each.
	envConfig := rest.CopyConfig(restConfig)
	envConfig.Impersonate = rest.ImpersonationConfig{UserName: controller.EnvironmentsUser}
	envWriter, err := client.New(envConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("environments client: %w", err)
	}
	if err := (&controller.Environments{
		Client: mgr.GetClient(), Writer: envWriter, Objects: s3c, Bucket: cfg.registryBucket,
		Recorder:        mgr.GetEventRecorderFor("maxcloud-controller"),
		MaxEnvironments: cfg.maxEnvironments, MaxPreviews: cfg.maxPreviews, Quota: cfg.previewQuota,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("environments: %w", err)
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

// newGateway is the API gateway towards the API server this controller
// talks to, with that connection's CA and none of its credentials.
func newGateway(restConfig *rest.Config, cfg config) (*gateway.Server, error) {
	upstream, err := url.Parse(restConfig.Host)
	if err != nil || upstream.Host == "" {
		return nil, fmt.Errorf("the API server's address %q isn't a URL", restConfig.Host)
	}
	tlsConfig, err := rest.TLSConfigFor(rest.AnonymousClientConfig(restConfig))
	if err != nil {
		return nil, err
	}
	return &gateway.Server{
		Addr:     cfg.gatewayAddr,
		Upstream: upstream,
		Transport: &http.Transport{
			TLSClientConfig:       tlsConfig,
			ForceAttemptHTTP2:     true,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: time.Minute,
		},
		Issuer: cfg.oidcIssuer, Audience: cfg.oidcClientID, UserPrefix: cfg.gatewayUserPrefix,
	}, nil
}
