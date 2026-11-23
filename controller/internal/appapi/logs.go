package appapi

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/maxemiliang/maxcloud/controller/internal/caller"
)

// Limits on a logs request: Loki keeps 30 days, and answers at most 5000
// lines per query (max_entries_limit_per_query).
const (
	maxLogRange    = 30 * 24 * time.Hour
	defaultLogLook = time.Hour
	defaultLogLim  = 1000
	maxLogLim      = 5000
)

// LogEntry is one line.
type LogEntry struct {
	Time      time.Time `json:"time"`
	Pod       string    `json:"pod"`
	Container string    `json:"container"`
	Line      string    `json:"line"`
}

// LogsResponse holds the newest lines in the range (up to the limit), oldest
// first. Limited says there were more.
type LogsResponse struct {
	App     string     `json:"app"`
	Start   time.Time  `json:"start"`
	End     time.Time  `json:"end"`
	Entries []LogEntry `json:"entries"`
	Limited bool       `json:"limited"`
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

// logQuery is the LogQL selector for an app's logs: always its namespace;
// within it, pods matching podRe, one container, or every container but
// Knative's queue-proxy unless all.
func logQuery(app, podRe, container string, all bool) string {
	q := `{namespace=` + strconv.Quote(app)
	if podRe != "" {
		q += `, pod=~` + strconv.Quote(podRe)
	}
	switch {
	case container != "":
		q += `, container=` + strconv.Quote(container)
	case !all:
		q += `, container!="queue-proxy"`
	}
	return q + `}`
}

func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	app, err := h.authorize(r, caller.Permission{Verb: "get", Resource: "pods", Subresource: "log"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	now := time.Now()
	end := now
	if s := q.Get("until"); s != "" {
		if end, err = parseTime(s, now); err != nil {
			h.fail(w, r, errorf(http.StatusBadRequest, "until: %v", err))
			return
		}
	}
	start := end.Add(-defaultLogLook)
	if s := q.Get("since"); s != "" {
		if start, err = parseTime(s, now); err != nil {
			h.fail(w, r, errorf(http.StatusBadRequest, "since: %v", err))
			return
		}
	}
	if !start.Before(end) {
		h.fail(w, r, errorf(http.StatusBadRequest, "since must be before until"))
		return
	}
	if end.Sub(start) > maxLogRange {
		h.fail(w, r, errorf(http.StatusBadRequest, "at most 30 days at once (Loki keeps 30)"))
		return
	}
	limit := defaultLogLim
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > maxLogLim {
			h.fail(w, r, errorf(http.StatusBadRequest, "limit: 1 to %d", maxLogLim))
			return
		}
	}
	podRe := q.Get("pod")
	if podRe != "" {
		if len(podRe) > 256 {
			h.fail(w, r, errorf(http.StatusBadRequest, "pod: at most 256 characters"))
			return
		}
		if _, err := regexp.Compile(podRe); err != nil {
			h.fail(w, r, errorf(http.StatusBadRequest, "pod: %v", err))
			return
		}
	}
	container := q.Get("container")
	if container != "" && (len(container) > 63 || !appNameRe.MatchString(container)) {
		h.fail(w, r, errorf(http.StatusBadRequest, "container: %q isn't a container name", container))
		return
	}

	var out struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][2]string       `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	err = h.getJSON(r.Context(), h.Loki, "/loki/api/v1/query_range", map[string]string{
		"query":     logQuery(app, podRe, container, q.Get("all") == "true"),
		"start":     strconv.FormatInt(start.UnixNano(), 10),
		"end":       strconv.FormatInt(end.UnixNano(), 10),
		"limit":     strconv.Itoa(limit),
		"direction": "backward",
	}, &out)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := LogsResponse{App: app, Start: start.UTC(), End: end.UTC(), Entries: []LogEntry{}}
	for _, s := range out.Data.Result {
		for _, v := range s.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			resp.Entries = append(resp.Entries, LogEntry{Time: time.Unix(0, ns).UTC(), Pod: s.Stream["pod"], Container: s.Stream["container"], Line: v[1]})
		}
	}
	sort.SliceStable(resp.Entries, func(i, j int) bool { return resp.Entries[i].Time.Before(resp.Entries[j].Time) })
	resp.Limited = len(resp.Entries) >= limit
	writeJSON(w, http.StatusOK, resp)
}
