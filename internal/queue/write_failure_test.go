package queue

import (
	"context"
	"database/sql"
	"errors"
	"local/fivemanage-discord-adapter/internal/config"
	"modernc.org/sqlite"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteFullRollbackAndRecovery(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults().Queue
	cfg.MinFreeBytes = 1
	q, err := Open(ctx, t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var pages int
	if err = q.conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err = q.conn.ExecContext(ctx, "PRAGMA max_page_count=5"); err != nil {
		t.Fatal(err)
	}
	e := Entry{ID: "synthetic-full", Source: "synthetic", Payload: []byte(strings.Repeat("x", 128<<10)), Received: time.Now()}
	if _, err = q.Enqueue(ctx, e); err == nil {
		t.Fatal("expected actual SQLite full error")
	} else {
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code() != 13 {
			t.Fatalf("expected SQLITE_FULL (13), got %v", err)
		}
	}
	s, err := q.Stats(ctx)
	if err != nil || s.Messages != 0 || s.Bytes != 0 {
		t.Fatal(s, err)
	}
	if _, err = q.conn.ExecContext(ctx, "PRAGMA max_page_count=100000"); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Enqueue(ctx, e); err != nil {
		t.Fatal("did not recover", err)
	}
	s, err = q.Stats(ctx)
	if err != nil || s.Messages != 1 || s.Bytes != 128<<10 {
		t.Fatal(s, err)
	}
	var check string
	if err = q.conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatal(check, err)
	}
}

func TestBusyCheckpointDoesNotStallAdmission(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults().Queue
	cfg.MinFreeBytes = 1
	dir := t.TempDir()
	q, err := Open(ctx, dir, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	e := Entry{ID: "snapshot", Source: "synthetic", Payload: []byte("synthetic"), Received: time.Now()}
	if _, err = q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", filepath.Join(dir, "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM pending").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err = q.Delete(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	busy, err := q.Maintain(ctx)
	if err != nil || !busy || time.Since(start) > time.Second {
		t.Fatal("checkpoint should return busy promptly", busy, err)
	}
	e.ID = "after-busy"
	if _, err = q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if busy, err = q.Maintain(ctx); err != nil || busy {
		t.Fatal(busy, err)
	}
}
