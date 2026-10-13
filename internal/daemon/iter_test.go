package daemon

import (
	"context"
	"testing"
	"time"
)

// TestLoopIterationLabels checks that instances of the SAME source segment in a
// loop body are distinguished by an iteration label (#1/#2/#3), and that nested
// loops produce dotted paths (#1.1/#2.2).
func TestLoopIterationLabels(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "for f in a b c do cat $f | wc -l end", nil)
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	flows, _ := j.Graph()
	seen := map[string]bool{}
	for _, f := range flows {
		if f.Kind == "pipe" && f.Iter != "" {
			seen[f.Iter] = true
		}
	}
	for _, want := range []string{"#1", "#2", "#3"} {
		if !seen[want] {
			t.Fatalf("missing iteration label %s (got %v)", want, seen)
		}
	}
}

func TestNestedLoopIterationLabels(t *testing.T) {
	s := newTestSession(t, time.Minute)
	j, err := s.StartJob(context.Background(), "for i in 1 2 do for j in a b do echo $i$j end end", nil)
	if err != nil {
		t.Fatal(err)
	}
	j.Wait()
	flows, _ := j.Graph()
	seen := map[string]bool{}
	for _, f := range flows {
		if f.Iter != "" {
			seen[f.Iter] = true
		}
	}
	for _, want := range []string{"#1.1", "#1.2", "#2.1", "#2.2"} {
		if !seen[want] {
			t.Fatalf("missing nested iteration label %s (got %v)", want, seen)
		}
	}
}
