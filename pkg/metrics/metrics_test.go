package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPush_IncludesLocationLabel(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &Pusher{URL: srv.URL}
	p.Push(Result{
		Snapshot: "d-immich",
		Location: "drobo",
		Success:  true,
		Duration: 60 * time.Second,
	})

	assert.Contains(t, body, `snapshot="d-immich"`)
	assert.Contains(t, body, `location="drobo"`)
	assert.Contains(t, body, `vrestic_backup_success`)
	assert.Contains(t, body, `vrestic_backup_duration_seconds`)
	assert.Contains(t, body, `vrestic_backup_last_run_timestamp`)
	assert.Contains(t, body, `vrestic_backup_last_success_timestamp`)
}

func TestPush_EmptyURL_NoOp(t *testing.T) {
	p := &Pusher{URL: ""}
	// Should not panic or make any HTTP calls
	p.Push(Result{Snapshot: "test", Location: "drobo", Success: true})
}

func TestPush_FailureOmitsSuccessTimestamp(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := &Pusher{URL: srv.URL}
	p.Push(Result{
		Snapshot: "d-immich",
		Location: "drobo",
		Success:  false,
		Duration: 10 * time.Second,
	})

	assert.Contains(t, body, `vrestic_backup_success`)
	assert.NotContains(t, body, `vrestic_backup_last_success_timestamp`)
}
