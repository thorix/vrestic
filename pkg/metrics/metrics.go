package metrics

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Pusher sends metrics to a Prometheus pushgateway-compatible endpoint.
// All methods are best-effort: failures are logged but never block backups.
type Pusher struct {
	URL string // e.g. http://vmagent:8429/api/v1/import/prometheus
	Job string // job label (default: "vrestic")
}

// Result holds the outcome of a single backup run
type Result struct {
	Snapshot string
	Success  bool
	Duration time.Duration
	Error    error
}

// Push sends metrics for a backup result. Safe to call with empty URL (no-op).
func (p *Pusher) Push(r Result) {
	if p.URL == "" {
		return
	}

	job := p.Job
	if job == "" {
		job = "vrestic"
	}

	now := time.Now().Unix()
	successVal := 0
	if r.Success {
		successVal = 1
	}

	var lines []string

	// Success gauge (1 = success, 0 = failure)
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_success{snapshot=%q,job=%q} %d %d`, r.Snapshot, job, successVal, now*1000),
	)

	// Duration in seconds
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_duration_seconds{snapshot=%q,job=%q} %.2f %d`, r.Snapshot, job, r.Duration.Seconds(), now*1000),
	)

	// Last run timestamp
	lines = append(lines,
		fmt.Sprintf(`vrestic_backup_last_run_timestamp{snapshot=%q,job=%q} %d %d`, r.Snapshot, job, now, now*1000),
	)

	// Last success timestamp (only on success)
	if r.Success {
		lines = append(lines,
			fmt.Sprintf(`vrestic_backup_last_success_timestamp{snapshot=%q,job=%q} %d %d`, r.Snapshot, job, now, now*1000),
		)
	}

	body := strings.Join(lines, "\n") + "\n"

	slog.Debug("Pushing metrics", "url", p.URL, "snapshot", r.Snapshot, "success", r.Success)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(p.URL, "text/plain", strings.NewReader(body))
	if err != nil {
		slog.Warn("Failed to push metrics", "snapshot", r.Snapshot, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		slog.Warn("Metrics push returned non-OK status", "snapshot", r.Snapshot, "status", resp.StatusCode)
	}
}
