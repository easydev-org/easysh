package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
)

// hashLen is the content-hash width. 16 bytes (128 bits) makes collisions
// negligible at any realistic scale while halving the manifest cost of a full
// sha256.
const hashLen = 16

// inlineMax is the largest part stored inline (as raw bytes) rather than as a
// hash reference. A part this small costs less to store than its hash, and tiny
// parts are not worth deduplicating. It equals hashLen so the two schemes meet
// at the same point: "store the bytes" wins exactly while len <= hashLen.
const inlineMax = hashLen

// key identifies a flow's assembly buffer: job + fid.
func key(jobID string, fid int64) string {
	// small alloc; fine for the write path
	return jobID + "/" + i64(fid)
}

func i64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// splitIntoParts feeds a chunk of stream bytes through the line assembler: it
// emits one blob+manifest entry per complete line, keeping any trailing partial
// line buffered until more bytes (or EOF) arrive. A line with no '\n' by
// maxPartBytes is emitted as a bounded chunk so binary input cannot buffer
// unboundedly.
func (s *Store) splitIntoParts(tx *sql.Tx, blobStmt, partStmt *sql.Stmt, jobID string, fid int64, data []byte) {
	k := key(jobID, fid)
	buf := append(s.part[k], data...)
	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			// no complete line yet
			if len(buf) >= maxPartBytes {
				s.emitPart(tx, blobStmt, partStmt, jobID, fid, buf[:maxPartBytes])
				buf = append([]byte(nil), buf[maxPartBytes:]...)
				continue
			}
			break
		}
		line := buf[:i+1] // include the terminating '\n'
		s.emitPart(tx, blobStmt, partStmt, jobID, fid, line)
		buf = append([]byte(nil), buf[i+1:]...)
	}
	s.part[k] = buf
	if len(buf) == 0 {
		delete(s.part, k)
	}
}

// emitPart stores one part's bytes as a content-addressed blob and appends its
// hash to the flow's manifest.
func (s *Store) emitPart(tx *sql.Tx, blobStmt, partStmt *sql.Stmt, jobID string, fid int64, part []byte) {
	if len(part) == 0 {
		return
	}
	k := key(jobID, fid)
	seq := s.partSeq[k]
	s.partSeq[k] = seq + 1
	if len(part) <= inlineMax {
		// Tiny part: store the bytes themselves (no hash, no dedup).
		if partStmt != nil {
			partStmt.Exec(jobID, fid, seq, nil, part)
		}
		return
	}
	// Larger part: content-address it so duplicate lines share storage.
	sum := sha256.Sum256(part)
	h := sum[:hashLen]
	if blobStmt != nil {
		blobStmt.Exec(h, len(part), part)
	}
	if partStmt != nil {
		partStmt.Exec(jobID, fid, seq, h, nil)
	}
}

// flushFlowAtEOF emits the trailing partial line (a stream that did not end in
// '\n') so nothing is lost.
func (s *Store) flushFlowAtEOF(tx *sql.Tx, blobStmt, partStmt *sql.Stmt, jobID string, fid int64) {
	k := key(jobID, fid)
	if buf := s.part[k]; len(buf) > 0 {
		s.emitPart(tx, blobStmt, partStmt, jobID, fid, buf)
		delete(s.part, k)
	}
}
