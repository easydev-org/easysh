package store

// FlowStore is the reusable half of the store: the dataflow graph
// (flow / flow_edge / flow_part / line_blob) on an EXISTING *sql.DB.
//
// It exists so the worker (github.com/easydev-org/easyworker) can persist the
// same graph into its own jobs.db without duplicating any of this SQL — the
// schema is deliberately shared (see the package comment). Store embeds the
// same logic for easysh's own use.
//
// A FlowStore does not own the connection: Open it with NewFlowStore(db) and
// Close the *sql.DB yourself.

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
)

// FlowStore reads and writes a job's flow graph.
type FlowStore struct {
	db *sql.DB
}

// NewFlowStore wraps an existing database. The flow tables are created if they
// are missing (idempotent), so a caller with its own migrate() can just call
// this.
func NewFlowStore(db *sql.DB) (*FlowStore, error) {
	fs := &FlowStore{db: db}
	if err := fs.Migrate(); err != nil {
		return nil, err
	}
	return fs, nil
}

// Migrate creates the flow tables (idempotent).
func (f *FlowStore) Migrate() error {
	for _, q := range flowSchema {
		if _, err := f.db.Exec(q); err != nil {
			return fmt.Errorf("flow migrate: %w", err)
		}
	}
	return nil
}

// flowSchema is the dataflow-graph DDL. Kept in one place so the worker and
// easysh cannot drift apart.
var flowSchema = []string{
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

// PutFlow records one flow row.
func (f *FlowStore) PutFlow(jobID string, r FlowRow) error {
	_, err := f.db.Exec(
		`INSERT OR REPLACE INTO flow (job_id, fid, kind, fd, owner, producer, consumer, path, dup_of, line, col, end_line, end_col, iter, bytes, lines, sha256, started_at, closed_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		jobID, r.FID, r.Kind, r.Fd, r.Owner, r.Producer, r.Consumer, r.Path, r.DupOf, r.Line,
		r.Col, r.EndLine, r.EndCol, r.Iter, r.Bytes, r.Lines, r.SHA256, r.StartedAt, r.ClosedAt)
	return err
}

// PutEdge records one flow edge.
func (f *FlowStore) PutEdge(jobID string, from, to int64) error {
	_, err := f.db.Exec(
		`INSERT OR REPLACE INTO flow_edge (job_id, from_fid, to_fid) VALUES (?,?,?)`,
		jobID, from, to)
	return err
}

// Flows returns a job's flow rows in fid order.
func (f *FlowStore) Flows(jobID string) ([]FlowRow, error) {
	rows, err := f.db.Query(
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
		var r FlowRow
		if err := rows.Scan(&r.FID, &r.Kind, &r.Fd, &r.Owner, &r.Producer, &r.Consumer,
			&r.Path, &r.DupOf, &r.Line, &r.Col, &r.EndLine, &r.EndCol, &r.Iter,
			&r.Bytes, &r.Lines, &r.SHA256, &r.StartedAt, &r.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Edges returns a job's flow edges.
func (f *FlowStore) Edges(jobID string) ([][2]int64, error) {
	rows, err := f.db.Query(`SELECT from_fid,to_fid FROM flow_edge WHERE job_id=? ORDER BY from_fid,to_fid`, jobID)
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

// PutBytes stores a whole flow's content in one shot (line-split, tiny parts
// inline, larger parts content-addressed). It is the simple synchronous path;
// easysh's Store instead streams chunks through its writer goroutine.
func (f *FlowStore) PutBytes(jobID string, fid int64, data []byte) error {
	tx, err := f.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var seq int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq),-1)+1 FROM flow_part WHERE job_id=? AND fid=?`, jobID, fid).Scan(&seq)
	for _, part := range splitLines(data) {
		if len(part) <= inlineMax {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO flow_part (job_id,fid,seq,hash,inline) VALUES (?,?,?,NULL,?)`, jobID, fid, seq, part); err != nil {
				return err
			}
		} else {
			sum := sha256.Sum256(part)
			h := sum[:hashLen]
			if _, err := tx.Exec(`INSERT OR IGNORE INTO line_blob (hash,len,data) VALUES (?,?,?)`, h, len(part), part); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO flow_part (job_id,fid,seq,hash,inline) VALUES (?,?,?,?,NULL)`, jobID, fid, seq, h); err != nil {
				return err
			}
		}
		seq++
	}
	return tx.Commit()
}

// Content reassembles a flow's bytes from its manifest.
func (f *FlowStore) Content(jobID string, fid int64) ([]byte, error) {
	rows, err := f.db.Query(
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

// DeleteJob drops a job's graph (retention).
func (f *FlowStore) DeleteJob(jobID string) error {
	for _, q := range []string{
		`DELETE FROM flow_part WHERE job_id=?`,
		`DELETE FROM flow_edge WHERE job_id=?`,
		`DELETE FROM flow WHERE job_id=?`,
	} {
		if _, err := f.db.Exec(q, jobID); err != nil {
			return err
		}
	}
	return nil
}

// splitLines cuts data at '\n' (keeping the terminator) and, for a run with no
// newline, at maxPartBytes so binary input cannot buffer unboundedly.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := -1
		for j := range data {
			if data[j] == '\n' {
				i = j
				break
			}
		}
		if i >= 0 {
			out = append(out, data[:i+1])
			data = data[i+1:]
			continue
		}
		if len(data) > maxPartBytes {
			out = append(out, data[:maxPartBytes])
			data = data[maxPartBytes:]
			continue
		}
		out = append(out, data)
		break
	}
	return out
}
