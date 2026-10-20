package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openLikeWorker uses the SAME DSN the worker uses, so the test exercises the
// real pragmas (WAL, busy_timeout=5000) rather than a convenience default.
func openLikeWorker(t *testing.T, path string) *sql.DB {
	t.Helper()
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(10)
	return db
}

// TestDeferredReadThenWriteHitsBusySnapshot pins the SQLite SEMANTICS that made
// PutBytes lose data: a DEFERRED transaction that reads first and writes later
// fails with SQLITE_BUSY_SNAPSHOT, which busy_timeout does NOT cover.
//
// It is written against raw SQL rather than PutBytes because PutBytes' own
// window is microseconds — too narrow to overlap reliably in a test. The
// transaction SHAPE is what matters, and this asserts it directly, so the reason
// PutBytes must use BEGIN IMMEDIATE is documented by an executable test.
func TestDeferredReadThenWriteHitsBusySnapshot(t *testing.T) {
	dir := t.TempDir()
	db := openLikeWorker(t, filepath.Join(dir, "snap.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE p (fid INTEGER, seq INTEGER)`); err != nil {
		t.Fatal(err)
	}

	// A: read-then-write, holding the transaction across a pause (the shape that
	// breaks). B commits in the middle of that pause.
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		tx, err := db.Begin() // DEFERRED
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback()
		var seq int64
		_ = tx.QueryRow(`SELECT COALESCE(MAX(seq),-1)+1 FROM p WHERE fid=1`).Scan(&seq)
		close(ready)
		time.Sleep(300 * time.Millisecond) // another writer commits here
		_, err = tx.Exec(`INSERT INTO p (fid,seq) VALUES (1,?)`, seq)
		if err == nil {
			err = tx.Commit()
		}
		done <- err
	}()

	<-ready
	if _, err := db.Exec(`INSERT INTO p (fid,seq) VALUES (2,0)`); err != nil {
		t.Fatalf("B write (should succeed, it is the only writer taking the lock): %v", err)
	}

	err := <-done
	if err == nil {
		t.Fatal("expected SQLITE_BUSY_SNAPSHOT; the deferred read-then-write should fail")
	}
	if !strings.Contains(err.Error(), "517") && !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Errorf("expected a busy/locked error, got: %v", err)
	}
}

// TestPutBytesSurvivesConcurrentWriters is the regression test for the live bug:
//
//	store: flow content <job>/4: database is locked (517) (SQLITE_BUSY)
//
// A DEFERRED transaction that reads first (SELECT MAX(seq)) fixes a snapshot; if
// another connection commits before the INSERT, the write fails immediately with
// SQLITE_BUSY_SNAPSHOT — busy_timeout does NOT cover that case, so the flow's
// content was silently lost and JobFlow returned 0 bytes for a flow the graph
// said held 201.
//
// Several writers, overlapping in time, with the read-then-write shape that
// PutBytes has. All of them must succeed.
func TestPutBytesSurvivesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	db := openLikeWorker(t, filepath.Join(dir, "flow.db"))
	defer db.Close()

	fs, err := NewFlowStore(db)
	if err != nil {
		t.Fatal(err)
	}

	const writers = 6
	// Overlap them: each starts a little after the previous, so several
	// read-then-write transactions are in flight at once (the failing shape).
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			time.Sleep(time.Duration(i) * 20 * time.Millisecond)
			// A payload big enough to be split into several parts, like a real
			// flow: several statements inside one transaction widen the window.
			payload := []byte("line one\nline two\nline three\nline four\nline five\n")
			if err := fs.PutBytes("job-1", int64(i+1), payload); err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("PutBytes failed under concurrency: %v", err)
	}

	// Every writer's content must be readable back, byte for byte.
	for i := 0; i < writers; i++ {
		got, err := fs.Content("job-1", int64(i+1))
		if err != nil {
			t.Fatalf("Content(f%d): %v", i+1, err)
		}
		want := "line one\nline two\nline three\nline four\nline five\n"
		if string(got) != want {
			t.Errorf("f%d content = %q, want %q", i+1, got, want)
		}
	}
}

// TestPutBytesRepeatedSameFidAppends: PutBytes continues the sequence for an
// existing fid rather than overwriting it — the REASON it must read MAX(seq)
// first, which is exactly what made the deferred transaction fragile.
func TestPutBytesRepeatedSameFidAppends(t *testing.T) {
	dir := t.TempDir()
	db := openLikeWorker(t, filepath.Join(dir, "flow.db"))
	defer db.Close()
	fs, err := NewFlowStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.PutBytes("job-1", 1, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := fs.PutBytes("job-1", 1, []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	got, err := fs.Content("job-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\nsecond\n" {
		t.Errorf("content = %q, want the two writes appended", got)
	}
}
