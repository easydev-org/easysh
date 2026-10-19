package store

import (
	"bytes"
	"testing"
	"time"
)

// TestLineDedupRoundTrip stores a stream whose lines repeat, and checks the
// exact bytes come back while duplicate large lines share one blob.
func TestLineDedupRoundTrip(t *testing.T) {
	st, err := Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// 3 lines: two identical long ones (must dedup) + a tiny one (must inline).
	content := []byte("a longer repeated line\nshort\n" + "a longer repeated line\n")
	st.EnqueueChunk("job1", 7, 0, content)
	st.EnqueueFlushFlow("job1", 7) // promote any trailing partial line
	time.Sleep(500 * time.Millisecond)

	got, err := st.Flow.Content("job1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("round-trip mismatch:\nwant %q\ngot  %q", content, got)
	}
	// The long line (25 B) is referenced once; the tiny one (6 B) is inline.
	if n := st.CountLineBlobs(); n != 1 {
		t.Fatalf("expected 1 unique blob, got %d", n)
	}
	// 3 manifest entries, all referenced/inline as expected.
	if n := st.CountFlowParts(); n != 3 {
		t.Fatalf("expected 3 parts, got %d", n)
	}
}

// TestPartialLinePreserved checks a stream without a trailing newline is stored
// and restored exactly (the trailing partial line must not be dropped).
func TestPartialLinePreserved(t *testing.T) {
	st, err := Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	content := []byte("first\nno trailing newline")
	st.EnqueueChunk("job2", 3, 0, content)
	st.EnqueueFlushFlow("job2", 3)
	time.Sleep(500 * time.Millisecond)

	got, err := st.Flow.Content("job2", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("partial line lost:\nwant %q\ngot  %q", content, got)
	}
}

// TestBytesAcrossChunks checks a line split across two writes is reassembled.
func TestBytesAcrossChunks(t *testing.T) {
	st, err := Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.EnqueueChunk("job3", 1, 0, []byte("hel"))
	st.EnqueueChunk("job3", 1, 1, []byte("lo world\nsecond line\n"))
	time.Sleep(500 * time.Millisecond)

	got, _ := st.Flow.Content("job3", 1)
	if string(got) != "hello world\nsecond line\n" {
		t.Fatalf("chunk-spanning line wrong: %q", got)
	}
}
