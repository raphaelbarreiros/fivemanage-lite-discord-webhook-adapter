package queue_test

import (
	"context"
	"errors"
	"fmt"
	"local/fivemanage-discord-adapter/internal/config"
	"local/fivemanage-discord-adapter/internal/queue"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func settings() config.Queue {
	c := config.Defaults().Queue
	c.MinFreeBytes = 1
	c.MaxDiskBytes = 1 << 30
	return c
}
func open(t *testing.T, c config.Queue) *queue.Queue {
	t.Helper()
	q, err := queue.Open(context.Background(), t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}
func insert(t *testing.T, q *queue.Queue, id, payload string, when time.Time) {
	t.Helper()
	_, err := q.Enqueue(context.Background(), queue.Entry{ID: id, Source: "synthetic", Payload: []byte(payload), Received: when})
	if err != nil {
		t.Fatal(err)
	}
}
func stats(t *testing.T, q *queue.Queue) queue.Stats {
	t.Helper()
	s, err := q.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestCapacityExpirationAndInFlightProtection(t *testing.T) {
	c := settings()
	c.MaxMessages = 2
	c.MaxBytes = 100
	c.Overflow = "drop_oldest"
	q := open(t, c)
	ctx := context.Background()
	now := time.Now()
	insert(t, q, "a", "1111", now.Add(-2*time.Second))
	insert(t, q, "b", "22222", now.Add(-time.Second))
	active, err := q.Reserve(ctx, now, func(int) time.Duration { return time.Second })
	if err != nil || active.ID != "a" {
		t.Fatal(active, err)
	}
	removed, err := q.Enqueue(ctx, queue.Entry{ID: "c", Source: "synthetic", Payload: []byte("333"), Received: now})
	if err != nil || removed.Overflow != 1 {
		t.Fatal(removed, err)
	}
	if s := stats(t, q); s.Messages != 2 || s.Bytes != 7 {
		t.Fatal(s)
	}
	if err = q.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	q.Release("a")
	next, err := q.Reserve(ctx, now.Add(time.Second), func(int) time.Duration { return time.Second })
	if err != nil || next.ID != "c" {
		t.Fatal(next, err)
	}
	q.Release("c")
	n, err := q.Expire(ctx, now.Add(c.MaxAge.Value()+time.Second))
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if s := stats(t, q); s.Messages != 0 || s.Bytes != 0 {
		t.Fatal(s)
	}
}
func TestRejectNewAndDiskReserveNeverAccept(t *testing.T) {
	c := settings()
	c.MaxMessages = 1
	q := open(t, c)
	now := time.Now()
	insert(t, q, "first", "payload", now)
	if _, err := q.Enqueue(context.Background(), queue.Entry{ID: "second", Payload: []byte("x"), Received: now}); !errors.Is(err, queue.ErrFull) {
		t.Fatal(err)
	}
	if s := stats(t, q); s.Messages != 1 || s.Bytes != 7 {
		t.Fatal(s)
	}
	c.MinFreeBytes = 1 << 62
	blocked := open(t, c)
	if _, err := blocked.Enqueue(context.Background(), queue.Entry{ID: "blocked", Payload: []byte("x"), Received: now}); !errors.Is(err, queue.ErrDisk) {
		t.Fatal(err)
	}
	if s := stats(t, blocked); s.Messages != 0 {
		t.Fatal(s)
	}
}
func TestConcurrentInsertDeleteMaintenance(t *testing.T) {
	q := open(t, settings())
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 50 {
				id := fmt.Sprintf("%d-%d", g, i)
				if _, err := q.Enqueue(ctx, queue.Entry{ID: id, Source: "s", Payload: []byte("test"), Received: time.Now()}); err != nil {
					errs <- err
					return
				}
				if err := q.Delete(ctx, id); err != nil {
					errs <- err
					return
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			if _, err := q.Maintain(ctx); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if s := stats(t, q); s.Messages != 0 || s.Bytes != 0 {
		t.Fatal(s)
	}
}
func TestReusesPagesAndShrinksAfterPeak(t *testing.T) {
	c := settings()
	c.VacuumPages = 256
	q := open(t, c)
	ctx := context.Background()
	payload := strings.Repeat("synthetic-data-", 4096)
	for i := range 100 {
		insert(t, q, fmt.Sprint(i), payload, time.Now())
	}
	q.Maintain(ctx)
	peak := stats(t, q)
	for i := range 100 {
		if err := q.Delete(ctx, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	deleted := stats(t, q)
	if deleted.FreePages == 0 {
		t.Fatal("delete should free pages")
	}
	for i := range 100 {
		insert(t, q, fmt.Sprint(i), payload, time.Now())
	}
	reused := stats(t, q)
	if reused.PageCount > peak.PageCount+10 {
		t.Fatalf("pages not reused: peak=%d reused=%d", peak.PageCount, reused.PageCount)
	}
	for i := range 100 {
		if err := q.Delete(ctx, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	for range 50 {
		if stats(t, q).FreePages == 0 {
			break
		}
		if _, err := q.Maintain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	final := stats(t, q)
	if final.DBBytes >= peak.DBBytes/2 || final.WALBytes != 0 || final.Messages != 0 {
		t.Fatalf("no reclamation: peak=%+v final=%+v", peak, final)
	}
	t.Logf("bytes: peak db=%d wal=%d free_pages=%d; deleted db=%d wal=%d free_pages=%d; reused pages=%d; final db=%d wal=%d free_pages=%d", peak.DBBytes, peak.WALBytes, peak.FreePages, deleted.DBBytes, deleted.WALBytes, deleted.FreePages, reused.PageCount, final.DBBytes, final.WALBytes, final.FreePages)
}
func TestAbruptProcessRestartKeepsCommittedAndScheduled(t *testing.T) {
	if os.Getenv("QUEUE_CRASH_CHILD") == "1" {
		q, err := queue.Open(context.Background(), os.Getenv("QUEUE_CRASH_DIR"), settings())
		if err != nil {
			os.Exit(2)
		}
		_, err = q.Enqueue(context.Background(), queue.Entry{ID: "stable", Source: "synthetic", Payload: []byte("committed payload"), Received: time.Now()})
		if err != nil {
			os.Exit(3)
		}
		q.Reserve(context.Background(), time.Now(), func(int) time.Duration { return time.Second })
		fmt.Println("committed")
		os.Exit(0)
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAbruptProcessRestartKeepsCommittedAndScheduled$")
	cmd.Env = append(os.Environ(), "QUEUE_CRASH_CHILD=1", "QUEUE_CRASH_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending.db-wal")); err != nil {
		t.Fatal("expected unclosed WAL", err)
	}
	q, err := queue.Open(context.Background(), dir, settings())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	e, err := q.Reserve(context.Background(), time.Now().Add(2*time.Second), func(int) time.Duration { return time.Second })
	if err != nil || e == nil || e.ID != "stable" || string(e.Payload) != "committed payload" || e.Attempts != 2 {
		t.Fatal(e, err)
	}
	if other, err := queue.Open(context.Background(), dir, settings()); err == nil {
		other.Close()
		t.Fatal("second instance admitted")
	}
}

func TestRetryDoesNotResetOriginalExpiration(t *testing.T) {
	c := settings()
	c.MaxAge = config.Duration(time.Hour)
	q := open(t, c)
	ctx := context.Background()
	received := time.Now().Add(-59 * time.Minute)
	insert(t, q, "age", "synthetic", received)
	e, err := q.Reserve(ctx, time.Now(), func(int) time.Duration { return time.Hour })
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	if err = q.Retry(ctx, e.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	q.Release(e.ID)
	n, err := q.Expire(ctx, received.Add(time.Hour+time.Second))
	if err != nil || n != 1 {
		t.Fatal("retry reset expiration", n, err)
	}
}
