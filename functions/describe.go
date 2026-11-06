package functions

import (
	"math"
	"runtime/debug"
	"sort"
)

// describeTag starts the description line printed for MAXCLOUD_DESCRIBE=1.
const describeTag = "maxcloud:describe:"

const modulePath = "github.com/maxemiliang/maxcloud/functions"

// The description mcloud generates manifests from; the same shape as the
// Node.js SDK's.
type description struct {
	SDK       string `json:"sdk"`
	Version   string `json:"version"`
	Functions []any  `json:"functions"`
	// The SQL migrations Start applies (file names).
	Migrations []string `json:"migrations"`
}

type describedBase struct {
	Name           string  `json:"name"`
	Export         string  `json:"export"`
	Kind           kind    `json:"kind"`
	Memory         *string `json:"memory"`
	CPU            *string `json:"cpu"`
	TimeoutSeconds float64 `json:"timeoutSeconds"`

	MemoryLimit            *string  `json:"memoryLimit"`
	User                   *int64   `json:"user"`
	ReadOnlyRootFilesystem bool     `json:"readOnlyRootFilesystem"`
	Volumes                []volume `json:"volumes"`
}

type describedHTTP struct {
	describedBase
	Public       bool   `json:"public"`
	MinInstances int    `json:"minInstances"`
	MaxInstances int    `json:"maxInstances"`
	Concurrency  int    `json:"concurrency"`
	Proxied      bool   `json:"proxied"`
	HealthPath   string `json:"healthPath"`
}

type describedSchedule struct {
	describedBase
	Schedule string `json:"schedule"`
	TimeZone string `json:"timeZone"`
	Retries  int    `json:"retries"`
}

type describedQueue struct {
	describedBase
	Queue          string `json:"queue"`
	Concurrency    int    `json:"concurrency"`
	MaxInstances   int    `json:"maxInstances"`
	MaxAttempts    int    `json:"maxAttempts"`
	BackoffSeconds int    `json:"backoffSeconds"`
	BacklogQuery   string `json:"backlogQuery"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func or[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// The defaults, filled in so mcloud never needs to know them.
const (
	defaultHTTPTimeout     = 300
	defaultScheduleTimeout = 3600
	defaultJobTimeout      = 300
)

func (f *function) timeoutSeconds(def float64) float64 {
	if f.opts.timeout <= 0 {
		return def
	}
	return math.Ceil(f.opts.timeout.Seconds())
}

func (f *function) jobTimeout() float64 { return f.timeoutSeconds(defaultJobTimeout) }

func describe() description {
	mu.Lock()
	defer mu.Unlock()
	d := description{SDK: modulePath, Version: sdkVersion(), Functions: []any{}}
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		f := registry[n]
		o := f.opts
		vols := o.volumes
		if vols == nil {
			vols = []volume{}
		}
		base := describedBase{Name: f.name, Export: f.name, Kind: f.kind, Memory: strPtr(o.memory), CPU: strPtr(o.cpu),
			MemoryLimit: strPtr(o.memoryLimit), User: o.user, ReadOnlyRootFilesystem: o.readOnlyRoot, Volumes: vols}
		switch f.kind {
		case kindHTTP:
			base.TimeoutSeconds = f.timeoutSeconds(defaultHTTPTimeout)
			d.Functions = append(d.Functions, describedHTTP{
				describedBase: base, Public: o.public, MinInstances: o.minInstances, MaxInstances: or(o.maxInstances, 3), Concurrency: o.concurrency,
				Proxied: o.proxied, HealthPath: o.healthPath,
			})
		case kindSchedule:
			base.TimeoutSeconds = f.timeoutSeconds(defaultScheduleTimeout)
			d.Functions = append(d.Functions, describedSchedule{base, f.schedule, or(o.timeZone, "UTC"), o.retries})
		case kindQueue:
			base.TimeoutSeconds = f.jobTimeout()
			d.Functions = append(d.Functions, describedQueue{base, f.queue, or(o.concurrency, 1), or(o.maxInstances, 3), or(o.maxAttempts, 5), int(or(o.backoff, defaultBackoff).Seconds()), BacklogQuery(f.queue)})
		}
	}
	return d
}

func sdkVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == modulePath {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	if bi.Main.Path == modulePath {
		return bi.Main.Version
	}
	return "unknown"
}
