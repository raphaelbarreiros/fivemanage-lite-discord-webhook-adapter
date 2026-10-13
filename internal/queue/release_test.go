package queue

import (
	"context"
	"local/fivemanage-discord-adapter/internal/config"
	"testing"
	"time"
)

func TestReleaseWhileIOGateBlockedResumesDrain(t *testing.T) {
	ctx := context.Background()
	q, err := Open(ctx, t.TempDir(), config.Defaults().Queue)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	now := time.Now()
	for _, id := range []string{"first", "second"} {
		if _, err = q.Enqueue(ctx, Entry{ID: id, Source: "synthetic", Payload: []byte(`{"content":"synthetic"}`), Received: now}); err != nil {
			t.Fatal(err)
		}
	}
	e, err := q.Reserve(ctx, now, func(int) time.Duration { return 0 })
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	q.gate <- struct{}{}
	admissionTimeout := 10 * time.Millisecond
	done := make(chan struct{})
	go func() { q.Release(e.ID); close(done) }()
	time.Sleep(3 * admissionTimeout)
	select {
	case <-done:
	default:
		<-q.gate
		<-done
		t.Fatal("release waited for the I/O gate")
	}
	<-q.gate
	for range 2 {
		next, err := q.Reserve(ctx, now.Add(time.Second), func(int) time.Duration { return 0 })
		if err != nil || next == nil {
			t.Fatalf("worker cannot resume: entry=%v err=%v", next, err)
		}
		if err = q.Delete(ctx, next.ID); err != nil {
			t.Fatal(err)
		}
		q.Release(next.ID)
	}
	s, err := q.Stats(ctx)
	if err != nil || s.Messages != 0 {
		t.Fatal(s, err)
	}
}
