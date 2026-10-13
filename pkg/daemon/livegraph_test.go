package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/easydev-org/easysh/pkg/store"
)

// TestGraphGrowsLive checks the graph is populated while the job runs, not just
// at the end (the WebUI relies on this).
func TestGraphGrowsLive(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "sleep 1; echo done | cat", nil)
	if err != nil {
		t.Fatal(err)
	}
	// While the job sleeps, the terminal fd flows should already exist.
	time.Sleep(300 * time.Millisecond)
	flows, _ := j.Graph()
	if len(flows) == 0 {
		t.Fatal("graph is empty while the job is still running")
	}
	j.Wait()
}

// TestSubscribeFlowLive checks a flow's bytes are streamed live to a subscriber.
func TestSubscribeFlowLive(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "for i in 1 2 3 do echo row$i; sleep 0.2 end", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Find the job's stdout fid (an fd endpoint owned by the job) once created.
	var stdoutFid int64
	for i := 0; i < 50; i++ {
		flows, _ := j.Graph()
		for _, f := range flows {
			if f.Kind == "fd" && f.Fd == "stdout" && f.Owner == "job" {
				stdoutFid = f.FID
			}
		}
		if stdoutFid != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stdoutFid == 0 {
		t.Fatal("no job stdout fd flow")
	}
	ch, cancel := j.SubscribeFlow(stdoutFid)
	defer cancel()
	var got strings.Builder
	deadline := time.After(3 * time.Second)
	for !strings.Contains(got.String(), "row3") {
		select {
		case b, ok := <-ch:
			if !ok {
				t.Fatalf("flow stream closed early; got %q", got.String())
			}
			got.Write(b)
		case <-deadline:
			t.Fatalf("did not stream live; got %q", got.String())
		}
	}
	j.Wait()
}

// TestGraphPersists checks the graph survives in SQLite for a finished job.
func TestGraphPersists(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := NewSession("s", t.TempDir(), []string{"PATH=/usr/bin:/bin"}, st, time.Minute)
	j, _ := s.StartJob(context.Background(), "echo hi | cat", nil)
	j.Wait()
	flows, err := st.Flows(j.ID)
	if err != nil || len(flows) == 0 {
		t.Fatalf("flows not persisted: %v %d", err, len(flows))
	}
	edges, _ := st.Edges(j.ID)
	if len(edges) == 0 {
		t.Fatal("edges not persisted")
	}
}
