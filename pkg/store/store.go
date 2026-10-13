// Package store persists jobs, their output, and their flow graph to SQLite.
// The schema is deliberately compatible with the worker's jobs.db (jobs /
// job_lines), extended with flow/flow_edge/flow_chunk so a command's dataflow
// graph survives the process.
package store

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// maxPartBytes bounds an in-progress line. A "line" is normally up to '\n', but
// a stream with no newlines (binary) would otherwise buffer unboundedly, so we
// cut it into chunks. This only affects dedup granularity, never correctness:
// parts are concatenated by index on read.
const maxPartBytes = 256 * 1024

// Store is the SQLite-backed job store.
type Store struct {
	db *sql.DB

	mu     sync.Mutex
	queue  []line
	done   chan struct{}
	closed bool
	tick   *time.Ticker

	// line-level assembly, owned by the writer goroutine:
	//   part["jobID|fid"] = bytes not yet terminated by '\n'
	//   partSeq["jobID|fid"] = next part index
	part    map[string][]byte
	partSeq map[string]int64
}

type line struct {
	jobID  string
	fid    int64 // 0 = plain job output (stdout/stderr), >0 = a flow chunk
	stream int   // 1 stdout, 2 stderr
	seq    int64
	data   []byte
	flush  bool // when true, emit the flow's trailing partial line (EOF)
}

// Open opens (creating if needed) the SQLite database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, done: make(chan struct{}), tick: time.NewTicker(200 * time.Millisecond)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	go s.writer()
	return s, nil
}

// Close flushes and closes the store.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	<-time.After(300 * time.Millisecond) // let the writer flush
	return s.db.Close()
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			command TEXT NOT NULL,
			namespace TEXT NOT NULL DEFAULT 'user',
			state TEXT NOT NULL,
			exit_code INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL,
			finished_at INTEGER NOT NULL DEFAULT 0,
			detached INTEGER NOT NULL DEFAULT 0
		)`,
		// A job's raw stdout/stderr (fid = 0).
		`CREATE TABLE IF NOT EXISTS job_lines (
			job_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			stream INTEGER NOT NULL,
			line TEXT NOT NULL,
			PRIMARY KEY (job_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_job_lines_stream ON job_lines(job_id, stream, seq)`,
		// The dataflow graph.
		`CREATE TABLE IF NOT EXISTS flow (
			job_id TEXT NOT NULL,
			fid INTEGER NOT NULL,
			kind TEXT NOT NULL,
			fd TEXT,
			owner TEXT,
			producer TEXT,
			consumer TEXT,
			path TEXT,
			dup_of INTEGER,
			line INTEGER,
			col INTEGER,
			end_line INTEGER,
			end_col INTEGER,
			iter TEXT,
			bytes INTEGER NOT NULL DEFAULT 0,
			lines INTEGER NOT NULL DEFAULT 0,
			sha256 TEXT,
			started_at INTEGER NOT NULL,
			closed_at INTEGER,
			PRIMARY KEY (job_id, fid)
		)`,
		`CREATE TABLE IF NOT EXISTS flow_edge (
			job_id TEXT NOT NULL,
			from_fid INTEGER NOT NULL,
			to_fid INTEGER NOT NULL,
			PRIMARY KEY (job_id, from_fid, to_fid)
		)`,
		// The intermediate results, line-level.
		//
		// A stream is stored as an ORDERED list of parts (its "manifest" in
		// flow_part). A part is either:
		//   · inline  — the raw bytes themselves, for parts no longer than
		//               inlineMax. Storing a hash for a tiny part costs more than
		//               the part (e.g. "0\n" is 2 B but a hash is 16 B), and
		//               tiny parts are not worth deduplicating.
		//   · a ref   — a 16-byte content hash, for larger parts; the bytes live
		//               once in line_blob, so duplicate lines share storage.
		// flow_part is NOT deduplicated: each flow carries its own full list.
		// Reads concatenate the parts (inline bytes or joined blobs).
		`CREATE TABLE IF NOT EXISTS line_blob (
			hash BLOB PRIMARY KEY,
			len INTEGER NOT NULL,
			data BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS flow_part (
			job_id TEXT NOT NULL,
			fid INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			hash BLOB,
			inline BLOB,
			PRIMARY KEY (job_id, fid, seq)
		)`,
		// Shell history: what the user typed, per session. Distinct from jobs
		// (what ran) — a line the user cancelled, or one that never started a
		// job, still belongs in history. Shared across every terminal attached
		// to the same session.
		`CREATE TABLE IF NOT EXISTS history (
			session TEXT NOT NULL,
			seq INTEGER NOT NULL,
			command TEXT NOT NULL,
			ts INTEGER NOT NULL,
			PRIMARY KEY (session, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_history_session ON history(session, seq)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// writer batches inserts on a channel-free buffer (non-blocking enqueue).
func (s *Store) writer() {
	batch := make([]line, 0, 500)
	s.part = map[string][]byte{}
	s.partSeq = map[string]int64{}

	flush := func() {
		if len(batch) == 0 {
			return
		}
		tx, err := s.db.Begin()
		if err != nil {
			batch = batch[:0]
			return
		}
		stmtLine, _ := tx.Prepare(`INSERT OR REPLACE INTO job_lines (job_id, seq, stream, line) VALUES (?,?,?,?)`)
		blobStmt, _ := tx.Prepare(`INSERT OR IGNORE INTO line_blob (hash, len, data) VALUES (?,?,?)`)
		partStmt, _ := tx.Prepare(`INSERT OR REPLACE INTO flow_part (job_id, fid, seq, hash, inline) VALUES (?,?,?,?,?)`)
		for _, l := range batch {
			if l.fid == 0 {
				if stmtLine != nil {
					stmtLine.Exec(l.jobID, l.seq, l.stream, string(l.data))
				}
			} else if l.flush {
				s.flushFlowAtEOF(tx, blobStmt, partStmt, l.jobID, l.fid)
			} else {
				s.splitIntoParts(tx, blobStmt, partStmt, l.jobID, l.fid, l.data)
			}
		}
		if stmtLine != nil {
			stmtLine.Close()
		}
		if blobStmt != nil {
			blobStmt.Close()
		}
		if partStmt != nil {
			partStmt.Close()
		}
		tx.Commit()
		batch = batch[:0]
	}
	for {
		select {
		case <-s.tick.C:
			s.mu.Lock()
			batch = append(batch, s.queue...)
			s.queue = s.queue[:0]
			s.mu.Unlock()
			// Flush every tick (200ms): the queue is only a staging buffer, so
			// waiting for 500 rows would delay small jobs indefinitely.
			flush()
		case <-s.done:
			s.mu.Lock()
			batch = append(batch, s.queue...)
			s.queue = s.queue[:0]
			s.mu.Unlock()
			flush()
			return
		}
	}
}

// EnqueueLine appends raw job output (fid = 0).
func (s *Store) EnqueueLine(jobID string, seq int64, stream int, data []byte) {
	s.mu.Lock()
	s.queue = append(s.queue, line{jobID: jobID, fid: 0, stream: stream, seq: seq, data: data})
	s.mu.Unlock()
}

// EnqueueChunk appends a flow's intermediate bytes.
func (s *Store) EnqueueChunk(jobID string, fid int64, seq int64, data []byte) {
	s.mu.Lock()
	s.queue = append(s.queue, line{jobID: jobID, fid: fid, seq: seq, data: data})
	s.mu.Unlock()
}

// EnqueueFlushFlow asks the writer to emit a flow's trailing partial line (a
// stream that did not end in '\n'), so nothing is lost when the job finishes.
func (s *Store) EnqueueFlushFlow(jobID string, fid int64) {
	s.mu.Lock()
	s.queue = append(s.queue, line{jobID: jobID, fid: fid, flush: true})
	s.mu.Unlock()
}

// EnqueueJob records a new job row (synchronous so the Jobs UI sees it at once).
func (s *Store) EnqueueJob(id, command, ns string, startedAt int64) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO jobs (id, command, namespace, state, exit_code, started_at, finished_at, detached) VALUES (?,?,?,?,?,?,0,0)`,
		id, command, ns, "running", 0, startedAt)
	return err
}

// FinishJob marks a job done.
func (s *Store) FinishJob(id, state string, exitCode int, finishedAt int64) {
	s.db.Exec(`UPDATE jobs SET state=?, exit_code=?, finished_at=? WHERE id=?`, state, exitCode, finishedAt, id)
}

// MarkDetached flags a job as background.
func (s *Store) MarkDetached(id string) {
	s.db.Exec(`UPDATE jobs SET detached=1, state='detached' WHERE id=?`, id)
}

// PutFlow records one flow row.
func (s *Store) PutFlow(jobID string, f FlowRow) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO flow (job_id, fid, kind, fd, owner, producer, consumer, path, dup_of, line, col, end_line, end_col, iter, bytes, lines, sha256, started_at, closed_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		jobID, f.FID, f.Kind, f.Fd, f.Owner, f.Producer, f.Consumer, f.Path, f.DupOf, f.Line,
		f.Col, f.EndLine, f.EndCol, f.Iter,
		f.Bytes, f.Lines, f.SHA256, f.StartedAt, f.ClosedAt)
	return err
}

// PutEdge records one flow edge.
func (s *Store) PutEdge(jobID string, from, to int64) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO flow_edge (job_id, from_fid, to_fid) VALUES (?,?,?)`, jobID, from, to)
	return err
}

// FlowRow is one row of the flow table.
type FlowRow struct {
	FID       int64
	Kind      string
	Fd        string
	Owner     string
	Producer  string
	Consumer  string
	Path      string
	DupOf     int64
	Line      int
	Col       int
	EndLine   int
	EndCol    int
	Iter      string
	Bytes     int64
	Lines     int64
	SHA256    string
	StartedAt int64
	ClosedAt  int64
}

// CountJobs returns the number of persisted jobs (for tests and diagnostics).
func (s *Store) CountJobs() int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n)
	return n
}

// CountFlowParts returns the number of persisted manifest entries (flow parts).
func (s *Store) CountFlowParts() int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM flow_part`).Scan(&n)
	return n
}

// CountLineBlobs returns the number of unique lines stored.
func (s *Store) CountLineBlobs() int {
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM line_blob`).Scan(&n)
	return n
}

// AppendHistory records one typed command for a session and returns its seq.
func (s *Store) AppendHistory(session, command string, ts int64) (int64, error) {
	var next int64
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM history WHERE session=?`, session).Scan(&next)
	_, err := s.db.Exec(`INSERT INTO history (session, seq, command, ts) VALUES (?,?,?,?)`,
		session, next, command, ts)
	return next, err
}

// History returns up to limit recent commands for a session, oldest first.
func (s *Store) History(session string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(
		`SELECT command FROM (SELECT seq, command FROM history WHERE session=? ORDER BY seq DESC LIMIT ?) ORDER BY seq ASC`,
		session, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FlowChunks returns the exact bytes of one flow by reading its manifest
// (flow_part) in order: inline parts are their own bytes, referenced parts are
// joined from line_blob. Duplicate large lines are stored once; small parts are
// inline; order and repeats are preserved by the manifest, so the result is
// byte-identical to what the flow carried.
func (s *Store) FlowChunks(jobID string, fid int64) ([]byte, error) {
	rows, err := s.db.Query(
		`SELECT COALESCE(p.inline, b.data) FROM flow_part p
		 LEFT JOIN line_blob b ON b.hash = p.hash
		 WHERE p.job_id=? AND p.fid=? ORDER BY p.seq`, jobID, fid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []byte
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, rows.Err()
}

// Flows returns the flow rows for a job (from the store).
func (s *Store) Flows(jobID string) ([]FlowRow, error) {
	rows, err := s.db.Query(
		`SELECT fid,kind,COALESCE(fd,''),COALESCE(owner,''),COALESCE(producer,''),COALESCE(consumer,''),
		        COALESCE(path,''),COALESCE(dup_of,0),COALESCE(line,0),
		        COALESCE(col,0),COALESCE(end_line,0),COALESCE(end_col,0),COALESCE(iter,''),
		        bytes,lines,COALESCE(sha256,''),started_at,COALESCE(closed_at,0)
		 FROM flow WHERE job_id=? ORDER BY fid`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlowRow
	for rows.Next() {
		var f FlowRow
		if err := rows.Scan(&f.FID, &f.Kind, &f.Fd, &f.Owner, &f.Producer, &f.Consumer,
			&f.Path, &f.DupOf, &f.Line, &f.Col, &f.EndLine, &f.EndCol, &f.Iter,
			&f.Bytes, &f.Lines, &f.SHA256, &f.StartedAt, &f.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Edges returns the flow edges for a job.
func (s *Store) Edges(jobID string) ([][2]int64, error) {
	rows, err := s.db.Query(`SELECT from_fid,to_fid FROM flow_edge WHERE job_id=? ORDER BY from_fid,to_fid`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, [2]int64{a, b})
	}
	return out, rows.Err()
}

// JobRow is one row of the jobs table.
type JobRow struct {
	ID         string
	Command    string
	Namespace  string
	State      string
	ExitCode   int
	StartedAt  int64
	FinishedAt int64
	Detached   bool
}

// QueryJobs returns the most recent jobs (newest first).
func (s *Store) QueryJobs(limit int) ([]JobRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT id,command,COALESCE(namespace,'user'),state,exit_code,started_at,COALESCE(finished_at,0),COALESCE(detached,0)
		 FROM jobs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobRow
	for rows.Next() {
		var j JobRow
		var det int
		if err := rows.Scan(&j.ID, &j.Command, &j.Namespace, &j.State, &j.ExitCode, &j.StartedAt, &j.FinishedAt, &det); err != nil {
			return nil, err
		}
		j.Detached = det != 0
		out = append(out, j)
	}
	return out, rows.Err()
}
