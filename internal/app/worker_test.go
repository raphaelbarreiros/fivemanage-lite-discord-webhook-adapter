package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"local/fivemanage-discord-adapter/internal/lite"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfirmedRemoteWithLocalDeleteFailureDuplicatesSameID(t *testing.T) {
	a, dir := setup(t)
	ctx := context.Background()
	var ids []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var logs []struct {
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&logs); err != nil {
			t.Error(err)
		}
		ids = append(ids, logs[0].Metadata["adapter_event_id"])
		w.Header().Set("X-Fivemanage-Ingest", lite.Confirmation)
		w.Write([]byte(`{"status":"ok","data":"ok"}`))
	}))
	defer s.Close()
	a.client = lite.New(s.URL, time.Second)
	defer a.Close()
	w := call(a, "POST", "/api/webhooks/test/"+syntheticToken, `{"content":"synthetic"}`, "application/json")
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("CREATE TRIGGER fail_delete BEFORE DELETE ON pending BEGIN SELECT RAISE(FAIL,'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	a.step(ctx)
	stats, err := a.Queue.Stats(ctx)
	if err != nil || stats.Messages != 1 {
		t.Fatal("lost unconfirmed local deletion", stats, err)
	}
	metrics := httptest.NewRecorder()
	a.AdminHandler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "adapter_delivery_seconds_count 1\n") {
		t.Fatal("latency denominator omitted a remote call whose local DELETE failed")
	}
	if _, err = db.Exec("DROP TRIGGER fail_delete"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE pending SET next_ms=0"); err != nil {
		t.Fatal(err)
	}
	a.step(ctx)
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatal("retry identity lost", ids)
	}
	stats, err = a.Queue.Stats(ctx)
	if err != nil || stats.Messages != 0 {
		t.Fatal(stats, err)
	}
}
func TestRetrySchedulingAndPermanentFailuresAreBounded(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()
	status := 503
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
	defer s.Close()
	a.client = lite.New(s.URL, time.Second)
	defer a.Close()
	call(a, "POST", "/api/webhooks/test/"+syntheticToken, `{"content":"synthetic"}`, "application/json")
	delay := a.step(ctx)
	if delay != time.Second || calls != 1 {
		t.Fatal(delay, calls)
	}
	a.step(ctx)
	if calls != 1 {
		t.Fatal("aggressive retry")
	}
	// Read eligible row using queue API at a future time to prove the durable schedule, then release.
	e, err := a.Queue.Reserve(ctx, time.Now().Add(2*time.Second), func(int) time.Duration { return time.Second })
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	a.Queue.Retry(ctx, e.ID, time.Now().Add(-time.Second))
	a.Queue.Release(e.ID)
	status = 400
	a.step(ctx)
	stats, err := a.Queue.Stats(ctx)
	if err != nil || stats.Messages != 0 || a.Metrics.Permanent.Load() != 1 {
		t.Fatal(stats, err)
	}
}

func TestAttemptLimitDeletesWithoutErrorArchive(t *testing.T) {
	a, _ := setup(t)
	a.Config.MaxAttempts = 1
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer s.Close()
	a.client = lite.New(s.URL, time.Second)
	defer a.Close()
	w := call(a, "POST", "/api/webhooks/test/"+syntheticToken, `{"content":"synthetic"}`, "application/json")
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	a.step(context.Background())
	stats, err := a.Queue.Stats(context.Background())
	if err != nil || stats.Messages != 0 || a.Metrics.Exhausted.Load() != 1 {
		t.Fatal(stats, err)
	}
}
