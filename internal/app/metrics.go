package app

import (
	"fmt"
	"io"
	"local/fivemanage-discord-adapter/internal/queue"
	"sync/atomic"
	"time"
)

type Metrics struct {
	Received, Accepted, Rejected, Delivered, Failures, Retries, Expired, Overflow, Permanent, Exhausted, LocalErrors, CheckpointBusy atomic.Uint64
	AdmissionNS, DeliveryNS, DeliveryCalls                                                                                           atomic.Uint64
}

func (m *Metrics) write(w io.Writer, s queue.Stats) {
	for name, n := range map[string]uint64{"received": m.Received.Load(), "accepted": m.Accepted.Load(), "rejected": m.Rejected.Load(), "delivered": m.Delivered.Load(), "delivery_failures": m.Failures.Load(), "retries": m.Retries.Load(), "local_errors": m.LocalErrors.Load(), "checkpoint_busy": m.CheckpointBusy.Load()} {
		fmt.Fprintf(w, "adapter_%s_total %d\n", name, n)
	}
	for reason, n := range map[string]uint64{"expired": m.Expired.Load(), "overflow": m.Overflow.Load(), "permanent": m.Permanent.Load(), "attempts": m.Exhausted.Load()} {
		fmt.Fprintf(w, "adapter_discarded_total{reason=%q} %d\n", reason, n)
	}
	fmt.Fprintf(w, "adapter_admission_seconds_sum %g\nadapter_admission_seconds_count %d\nadapter_delivery_seconds_sum %g\nadapter_delivery_seconds_count %d\n", float64(m.AdmissionNS.Load())/1e9, m.Received.Load(), float64(m.DeliveryNS.Load())/1e9, m.DeliveryCalls.Load())
	oldest := 0.0
	if !s.Oldest.IsZero() {
		oldest = max(0, time.Since(s.Oldest).Seconds())
	}
	fmt.Fprintf(w, "adapter_pending_messages %d\nadapter_pending_payload_bytes %d\nadapter_db_bytes %d\nadapter_wal_bytes %d\nadapter_shm_bytes %d\nadapter_disk_free_bytes %d\nadapter_free_pages %d\nadapter_page_count %d\nadapter_page_size %d\nadapter_oldest_seconds %g\n", s.Messages, s.Bytes, s.DBBytes, s.WALBytes, s.SHMBytes, s.FreeBytes, s.FreePages, s.PageCount, s.PageSize, oldest)
}
