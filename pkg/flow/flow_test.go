package flow

import "testing"

func TestPipeIdentity(t *testing.T) {
	r := NewRegistry()
	sink := NewMemSink()
	prod := &CommandRef{Argv: []string{"a"}}
	cons := &CommandRef{Argv: []string{"b"}}
	w, rd := r.NewPipe(prod, cons, true, sink, 100)
	if w.FID == 0 || rd.FID == 0 {
		t.Fatal("pipe ends must have fids")
	}
	if w.FID != rd.FID {
		t.Fatalf("pipe ends must share one fid: %d vs %d", w.FID, rd.FID)
	}
	// Writing through the write end captures into the sink under that fid.
	if _, err := w.W.Write([]byte("hello\nworld\n")); err != nil {
		t.Fatal(err)
	}
	got := string(sink.Bytes(w.FID))
	if got != "hello\nworld\n" {
		t.Fatalf("captured = %q", got)
	}
	f := r.Get(w.FID)
	b, l := f.Counts()
	if b != 12 || l != 2 {
		t.Fatalf("counts = %d bytes, %d lines", b, l)
	}
}

func TestFileFlow(t *testing.T) {
	r := NewRegistry()
	dir := t.TempDir()
	path := dir + "/out.txt"
	s, err := r.NewFile(path, 0x241 /*O_WRONLY|O_CREATE|O_TRUNC*/, 0o644, &CommandRef{Argv: []string{"x"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	s.W.Write([]byte("data\n"))
	s.File.Close()
	f := r.Get(s.FID)
	if f.Kind != File || f.Path != path {
		t.Fatalf("flow = %+v", f)
	}
	if b, l := f.Counts(); b != 5 || l != 1 {
		t.Fatalf("counts = %d/%d", b, l)
	}
}

func TestDupEdge(t *testing.T) {
	r := NewRegistry()
	sink := NewMemSink()
	w, _ := r.NewPipe(&CommandRef{Argv: []string{"a"}}, &CommandRef{Argv: []string{"b"}}, false, sink, 10)
	d := r.Dup(w, &CommandRef{Argv: []string{"a"}})
	from := r.Get(w.FID)
	found := false
	for _, o := range from.Outputs {
		if o == d.FID {
			found = true
		}
	}
	if !found {
		t.Fatalf("dup should create an edge from src to dup")
	}
}

func TestEdge(t *testing.T) {
	r := NewRegistry()
	sink := NewMemSink()
	w1, _ := r.NewPipe(&CommandRef{Argv: []string{"a"}}, &CommandRef{Argv: []string{"b"}}, false, sink, 10)
	w2, _ := r.NewPipe(&CommandRef{Argv: []string{"b"}}, &CommandRef{Argv: []string{"c"}}, false, sink, 10)
	r.LinkByID(w1.FID, w2.FID)
	if len(r.Get(w1.FID).Outputs) != 1 || r.Get(w1.FID).Outputs[0] != w2.FID {
		t.Fatal("edge not recorded")
	}
	if len(r.Get(w2.FID).Inputs) != 1 || r.Get(w2.FID).Inputs[0] != w1.FID {
		t.Fatal("reverse edge not recorded")
	}
}
