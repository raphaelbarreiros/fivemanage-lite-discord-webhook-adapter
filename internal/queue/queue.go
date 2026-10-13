// Package queue owns one SQLite connection and serializes short operations.
// No transaction or database gate is held across remote HTTP calls.
package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"local/fivemanage-discord-adapter/internal/config"
	_ "modernc.org/sqlite"
)

var (
	ErrFull = errors.New("queue capacity reached")
	ErrDisk = errors.New("queue disk reserve reached")
	ErrBusy = errors.New("queue busy")
)

type Entry struct {
	ID, Source string
	Payload    []byte
	Received   time.Time
	Attempts   int
	Next       time.Time
}
type Stats struct {
	Messages, Bytes, DBBytes, WALBytes, SHMBytes, FreePages, PageCount, PageSize int64
	FreeBytes                                                                    uint64
	Oldest                                                                       time.Time
	SQLiteVersion                                                                string
}
type Removed struct{ Expired, Overflow int64 }
type Queue struct {
	db           *sql.DB
	conn         *sql.Conn
	lock         *os.File
	gate         chan struct{}
	dir, path    string
	cfg          config.Queue
	count, bytes int64
	active       atomic.Pointer[string]
	version      string
}

func Open(ctx context.Context, dir string, cfg config.Queue) (q *Queue, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("data directory must be a real local directory")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	q = &Queue{dir: abs, path: filepath.Join(abs, "pending.db"), cfg: cfg, gate: make(chan struct{}, 1)}
	q.lock, err = os.OpenFile(filepath.Join(abs, "instance.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	opened := q
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	if err = unix.Flock(int(q.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, errors.New("data directory already owned by another instance")
	}
	// Refuse a database symlink rather than chmod/open an unrelated target.
	if fi, e := os.Lstat(q.path); e == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database symlinks are not supported")
	}
	f, err := os.OpenFile(q.path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(q.path, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: q.path}
	v := url.Values{}
	for _, s := range []string{"busy_timeout=" + fmt.Sprint(cfg.BusyTimeout.Value().Milliseconds()), "synchronous=FULL", "foreign_keys=ON"} {
		v.Add("_pragma", s)
	}
	u.RawQuery = v.Encode()
	q.db, err = sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	q.db.SetMaxOpenConns(1)
	q.db.SetMaxIdleConns(1)
	q.conn, err = q.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var tables int
	if err = q.conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
		return nil, err
	}
	if tables == 0 {
		if _, err = q.conn.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
			return nil, err
		}
	}
	var mode string
	if err = q.conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		return nil, errors.New("WAL not available")
	}
	if _, err = q.conn.ExecContext(ctx, "PRAGMA wal_autocheckpoint=1000"); err != nil {
		return nil, err
	}
	if _, err = q.conn.ExecContext(ctx, "PRAGMA journal_size_limit=4194304"); err != nil {
		return nil, err
	}
	for pragma, want := range map[string]int{"synchronous": 2, "auto_vacuum": 2, "foreign_keys": 1, "busy_timeout": int(cfg.BusyTimeout.Value().Milliseconds())} {
		var got int
		if err = q.conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			return nil, err
		}
		if got != want {
			return nil, fmt.Errorf("SQLite pragma %s: expected %d, got %d", pragma, want, got)
		}
	}
	if err = q.conn.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&q.version); err != nil {
		return nil, err
	}
	// Fail closed if a dependency update changes the engine without revalidation.
	if q.version != "3.53.4" {
		return nil, errors.New("unvalidated SQLite engine version")
	}
	var schema int
	if err = q.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return nil, err
	}
	if schema != 0 && schema != 1 {
		return nil, errors.New("unsupported queue schema")
	}
	_, err = q.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS pending (
 id TEXT PRIMARY KEY NOT NULL, source TEXT NOT NULL, payload BLOB NOT NULL,
 received_ms INTEGER NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_ms INTEGER NOT NULL
 );
 CREATE INDEX IF NOT EXISTS pending_due ON pending(next_ms, received_ms);
 CREATE INDEX IF NOT EXISTS pending_age ON pending(received_ms);
 PRAGMA user_version=1;`)
	if err != nil {
		return nil, err
	}
	if err = q.conn.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(length(payload)),0) FROM pending").Scan(&q.count, &q.bytes); err != nil {
		return nil, err
	}
	return q, nil
}
func (q *Queue) Close() error {
	var err error
	if q.conn != nil {
		err = q.conn.Close()
	}
	if q.db != nil {
		err = errors.Join(err, q.db.Close())
	}
	if q.lock != nil {
		err = errors.Join(err, q.lock.Close())
	}
	return err
}
func (q *Queue) enter(ctx context.Context) error {
	select {
	case q.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ErrBusy
	}
}
func (q *Queue) leave() { <-q.gate }
func (q *Queue) disk() (s Stats, err error) {
	var fs unix.Statfs_t
	if err = unix.Statfs(q.dir, &fs); err != nil {
		return s, err
	}
	s.FreeBytes = uint64(fs.Bavail) * uint64(fs.Bsize)
	for path, out := range map[string]*int64{q.path: &s.DBBytes, q.path + "-wal": &s.WALBytes, q.path + "-shm": &s.SHMBytes} {
		f, e := os.Stat(path)
		if e == nil {
			*out = f.Size()
		} else if !os.IsNotExist(e) {
			return s, e
		}
	}
	return s, nil
}
func (q *Queue) admissible(size int64) error {
	s, err := q.disk()
	if err != nil {
		return err
	}
	// Reserve estimated transient pages/WAL overhead, not a promised physical cap.
	margin := uint64(size)*4 + 65536
	if s.FreeBytes < q.cfg.MinFreeBytes+margin || s.DBBytes+s.WALBytes+s.SHMBytes+int64(margin) > q.cfg.MaxDiskBytes {
		return ErrDisk
	}
	return nil
}
func (q *Queue) Enqueue(ctx context.Context, e Entry) (removed Removed, err error) {
	if err = q.enter(ctx); err != nil {
		return
	}
	defer q.leave()
	size := int64(len(e.Payload))
	if size > q.cfg.MaxBytes {
		return removed, ErrFull
	}
	if err = q.admissible(size); err != nil {
		return
	}
	tx, err := q.conn.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	cutoff := e.Received.Add(-q.cfg.MaxAge.Value()).UnixMilli()
	expired, n, err := q.deleteBatch(ctx, tx, "received_ms <= ? AND id != ?", []any{cutoff, q.activeID()}, 128)
	if err != nil {
		return removed, err
	}
	count, bytes := q.count-expired, q.bytes-n
	removed.Expired = expired
	if count+1 > q.cfg.MaxMessages || bytes+size > q.cfg.MaxBytes {
		if q.cfg.Overflow != "drop_oldest" {
			return Removed{}, ErrFull
		}
		rows, er := tx.QueryContext(ctx, "SELECT id,length(payload) FROM pending WHERE id != ? ORDER BY received_ms,id LIMIT 128", q.activeID())
		if er != nil {
			return Removed{}, er
		}
		type victim struct {
			id   string
			size int64
		}
		var victims []victim
		for rows.Next() {
			var v victim
			if er = rows.Scan(&v.id, &v.size); er != nil {
				rows.Close()
				return Removed{}, er
			}
			victims = append(victims, v)
		}
		er = rows.Err()
		rows.Close()
		if er != nil {
			return Removed{}, er
		}
		for _, v := range victims {
			if count+1 <= q.cfg.MaxMessages && bytes+size <= q.cfg.MaxBytes {
				break
			}
			if _, er = tx.ExecContext(ctx, "DELETE FROM pending WHERE id=?", v.id); er != nil {
				return Removed{}, er
			}
			count--
			bytes -= v.size
			removed.Overflow++
		}
		if count+1 > q.cfg.MaxMessages || bytes+size > q.cfg.MaxBytes {
			return Removed{}, ErrFull
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO pending(id,source,payload,received_ms,next_ms) VALUES(?,?,?,?,?)", e.ID, e.Source, e.Payload, e.Received.UnixMilli(), e.Received.UnixMilli())
	if err != nil {
		return Removed{}, err
	}
	if err = tx.Commit(); err != nil {
		return Removed{}, err
	}
	q.count = count + 1
	q.bytes = bytes + size
	return removed, nil
}
func (q *Queue) deleteBatch(ctx context.Context, tx *sql.Tx, where string, args []any, limit int) (count, bytes int64, err error) {
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, "DELETE FROM pending WHERE id IN (SELECT id FROM pending WHERE "+where+" ORDER BY received_ms LIMIT ?) RETURNING length(payload)", args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			return
		}
		count++
		bytes += n
	}
	err = rows.Err()
	return
}

// Reserve persists the next eligible time before HTTP. There is no durable processing state.
func (q *Queue) Reserve(ctx context.Context, now time.Time, delay func(int) time.Duration) (e *Entry, err error) {
	if err = q.enter(ctx); err != nil {
		return
	}
	defer q.leave()
	if q.active.Load() != nil {
		return nil, ErrBusy
	}
	var v Entry
	var received, next int64
	err = q.conn.QueryRowContext(ctx, "SELECT id,source,payload,received_ms,attempts,next_ms FROM pending WHERE next_ms<=? AND received_ms>? ORDER BY next_ms,received_ms LIMIT 1", now.UnixMilli(), now.Add(-q.cfg.MaxAge.Value()).UnixMilli()).Scan(&v.ID, &v.Source, &v.Payload, &received, &v.Attempts, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return
	}
	v.Received = time.UnixMilli(received).UTC()
	v.Attempts++
	v.Next = now.Add(delay(v.Attempts))
	_, err = q.conn.ExecContext(ctx, "UPDATE pending SET attempts=?,next_ms=? WHERE id=?", v.Attempts, v.Next.UnixMilli(), v.ID)
	if err != nil {
		return nil, err
	}
	q.active.Store(&v.ID)
	return &v, nil
}
func (q *Queue) activeID() string {
	if active := q.active.Load(); active != nil {
		return *active
	}
	return ""
}

// Release performs no I/O and cannot be canceled or blocked by a database operation.
func (q *Queue) Release(id string) {
	if active := q.active.Load(); active != nil && *active == id {
		q.active.CompareAndSwap(active, nil)
	}
}
func (q *Queue) Retry(ctx context.Context, id string, next time.Time) error {
	if err := q.enter(ctx); err != nil {
		return err
	}
	defer q.leave()
	_, err := q.conn.ExecContext(ctx, "UPDATE pending SET next_ms=? WHERE id=?", next.UnixMilli(), id)
	return err
}
func (q *Queue) Delete(ctx context.Context, id string) error {
	if err := q.enter(ctx); err != nil {
		return err
	}
	defer q.leave()
	var n int64
	err := q.conn.QueryRowContext(ctx, "DELETE FROM pending WHERE id=? RETURNING length(payload)", id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil {
		q.count--
		q.bytes -= n
	}
	return err
}
func (q *Queue) Expire(ctx context.Context, now time.Time) (int64, error) {
	if err := q.enter(ctx); err != nil {
		return 0, err
	}
	defer q.leave()
	tx, err := q.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, b, err := q.deleteBatch(ctx, tx, "received_ms<=? AND id!=?", []any{now.Add(-q.cfg.MaxAge.Value()).UnixMilli(), q.activeID()}, 128)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	q.count -= n
	q.bytes -= b
	return n, nil
}
func (q *Queue) Stats(ctx context.Context) (s Stats, err error) {
	if err = q.enter(ctx); err != nil {
		return
	}
	defer q.leave()
	s, err = q.disk()
	if err != nil {
		return
	}
	s.Messages = q.count
	s.Bytes = q.bytes
	s.SQLiteVersion = q.version
	for name, v := range map[string]*int64{"freelist_count": &s.FreePages, "page_count": &s.PageCount, "page_size": &s.PageSize} {
		if err = q.conn.QueryRowContext(ctx, "PRAGMA "+name).Scan(v); err != nil {
			return
		}
	}
	var oldest sql.NullInt64
	err = q.conn.QueryRowContext(ctx, "SELECT min(received_ms) FROM pending").Scan(&oldest)
	if oldest.Valid {
		s.Oldest = time.UnixMilli(oldest.Int64).UTC()
	}
	return
}
func (q *Queue) Ready(ctx context.Context) error {
	if err := q.enter(ctx); err != nil {
		return err
	}
	defer q.leave()
	if q.cfg.Overflow == "reject_new" && (q.count >= q.cfg.MaxMessages || q.bytes >= q.cfg.MaxBytes) {
		return ErrFull
	}
	return q.admissible(0)
}

// Maintenance uses PASSIVE and zero-wait TRUNCATE; busy readers are normal, not an error.
func (q *Queue) Maintain(ctx context.Context) (busy bool, err error) {
	if err = q.enter(ctx); err != nil {
		return
	}
	defer q.leave()
	var code, frames, done int
	if err = q.conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&code, &frames, &done); err != nil {
		return
	}
	if _, err = q.conn.ExecContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", q.cfg.VacuumPages)); err != nil {
		return
	}
	if _, err = q.conn.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
		return
	}
	defer func() {
		_, e := q.conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout=%d", q.cfg.BusyTimeout.Value().Milliseconds()))
		err = errors.Join(err, e)
	}()
	err = q.conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&code, &frames, &done)
	return code != 0, err
}
