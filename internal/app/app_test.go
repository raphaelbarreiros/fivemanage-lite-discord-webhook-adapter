package app

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"local/fivemanage-discord-adapter/internal/config"
	"local/fivemanage-discord-adapter/internal/queue"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const syntheticToken = "synthetic-test-not-a-real-secret"

func setup(t *testing.T) (*App, string) {
	t.Helper()
	c := config.Defaults()
	c.LiteURL = "http://127.0.0.1:1"
	c.DataDir = t.TempDir()
	c.Queue.MinFreeBytes = 1
	c.MaxBodyBytes = 1024
	c.Sources = []config.Source{{ID: "test", Secret: syntheticToken, LiteToken: syntheticToken, Resource: "trusted", Dataset: "trusted"}}
	q, err := queue.Open(context.Background(), c.DataDir, c.Queue)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return New(c, q, slog.New(slog.NewTextHandler(io.Discard, nil))), c.DataDir
}
func call(a *App, method, path, body, ct string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", ct)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

type commitObserver struct {
	*httptest.ResponseRecorder
	onCommit func()
}

func (w commitObserver) WriteHeader(status int) {
	if status == http.StatusNoContent {
		w.onCommit()
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestAdmissionCommitsBefore204AndRejectsUnsupported(t *testing.T) {
	a, dir := setup(t)
	path := "/api/webhooks/test/" + syntheticToken
	cases := []struct {
		method, path, body, ct string
		want                   int
	}{
		{"POST", path, `{"content":"synthetic"}`, "application/json", 204},
		{"POST", path + "?wait=false", `{"embeds":[{"future":true}]}`, "application/json", 204},
		{"POST", "/api/webhooks/test/invalid", `{"content":"x"}`, "application/json", 401},
		{"POST", path, `{"content":`, "application/json", 400},
		{"POST", path, `{"content":"` + strings.Repeat("x", 1024) + `"}`, "application/json", 413},
		{"POST", path + "?wait=true", `{"content":"x"}`, "application/json", 400},
		{"POST", path + "?thread_id=42", `{"content":"x"}`, "application/json", 400},
		{"POST", path + "?wait=false&wait=true", `{"content":"x"}`, "application/json", 400},
		{"POST", path, `{"content":"x","attachments":[]}`, "application/json", 400},
		{"POST", path, "multipart bytes", "multipart/form-data; boundary=x", 415},
		{"PATCH", path, `{"content":"x"}`, "application/json", 405},
		{"DELETE", path + "/messages/42", "", "application/json", 404},
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	accepted := 0
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ct)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(commitObserver{w, func() {
			// An independent connection must see the commit at the exact 204 boundary,
			// not merely after ServeHTTP has returned.
			var count int
			if err := db.QueryRow("SELECT count(*) FROM pending").Scan(&count); err != nil || count != accepted+1 {
				t.Fatal("204 preceded committed row", count, err)
			}
		}}, r)
		if w.Code != c.want {
			t.Fatalf("status=%d want=%d body=%s", w.Code, c.want, w.Body.String())
		}
		if c.want == 204 {
			accepted++
		}
	}
	if a.Metrics.Accepted.Load() != 2 || a.Metrics.Rejected.Load() != 10 {
		t.Fatal("incorrect counters")
	}
}
func TestLocalLockFailsAdmissionThenRecovers(t *testing.T) {
	a, dir := setup(t)
	db, err := sql.Open("sqlite", filepath.Join(dir, "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	path := "/api/webhooks/test/" + syntheticToken
	start := time.Now()
	w := call(a, "POST", path, `{"content":"synthetic locked"}`, "application/json")
	if w.Code != 503 || time.Since(start) > 2*time.Second {
		t.Fatal("lock did not fail boundedly", w.Code)
	}
	conn.ExecContext(context.Background(), "ROLLBACK")
	if w = call(a, "POST", path, `{"content":"synthetic recovered"}`, "application/json"); w.Code != 204 {
		t.Fatal(w.Code)
	}
	s, err := a.Queue.Stats(context.Background())
	if err != nil || s.Messages != 1 {
		t.Fatal(s, err)
	}
}
func TestAdmissionAndShutdownRedactSecrets(t *testing.T) {
	a, _ := setup(t)
	var logs bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	a.StopAdmissions()
	w := call(a, "POST", "/api/webhooks/test/"+syntheticToken, `{"content":"sensitive synthetic body"}`, "application/json")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code)
	}
	if strings.Contains(logs.String(), syntheticToken) || strings.Contains(logs.String(), "sensitive synthetic body") {
		t.Fatal("sensitive logging")
	}
}
