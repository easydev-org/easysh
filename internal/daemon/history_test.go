package daemon

import (
	"testing"
	"time"

	"github.com/abcp-sdk/easysh/internal/store"
)

// TestHistorySharedAcrossSessions checks that two Session objects with the same
// name (i.e. two terminals on one session) share history via the store.
func TestHistorySharedAcrossSessions(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	s1 := NewSession("shared", t.TempDir(), []string{"PATH=/usr/bin:/bin"}, st, time.Minute)
	s1.AddHistory("echo one")
	s1.AddHistory("echo two")

	// A second session with the same name loads the persisted history.
	s2 := NewSession("shared", t.TempDir(), []string{"PATH=/usr/bin:/bin"}, st, time.Minute)
	h := s2.History()
	if len(h) != 2 || h[0] != "echo one" || h[1] != "echo two" {
		t.Fatalf("history not shared: %#v", h)
	}
}

// TestHistoryDistinctSessions checks sessions do not bleed into each other.
func TestHistoryDistinctSessions(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := NewSession("a", t.TempDir(), nil, st, time.Minute)
	a.AddHistory("in-a")
	b := NewSession("b", t.TempDir(), nil, st, time.Minute)
	if got := b.History(); len(got) != 0 {
		t.Fatalf("session b should have no history, got %#v", got)
	}
}

// TestHistoryNoStoreStillWorks checks the in-memory path when there is no store.
func TestHistoryNoStoreStillWorks(t *testing.T) {
	s := newTestSession(t, time.Minute)
	s.AddHistory("x")
	s.AddHistory("y")
	if h := s.History(); len(h) != 2 {
		t.Fatalf("in-memory history: %#v", h)
	}
}
