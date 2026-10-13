package daemon

import (
	"github.com/easydev-org/easysh/pkg/flow"
)

// liveFlow is a subscriber to one flow's bytes (the WebUI's per-flow watch).
// ch is buffered and writes are dropped when full, so a slow watcher can never
// block the job.
type liveFlow struct {
	fid int64
	ch  chan []byte
}

// addFlow records a flow as soon as it is created, so the graph grows live.
func (j *Job) addFlow(f *flow.Flow) {
	snap := flowSnapshot{
		FID:      int64(f.ID),
		Kind:     f.Kind.String(),
		Producer: f.Producer.Name(),
		Consumer: f.Consumer.Name(),
		Path:     f.Path,
	}
	// The stage's source span (from its owning CommandRef) so the UI can
	// highlight which segment of the pipeline this flow belongs to.
	ref := f.Owner
	if ref == nil {
		ref = f.Producer
	}
	if ref != nil {
		snap.Line, snap.Col, snap.EndLine, snap.EndCol = ref.Line, ref.Col, ref.EndLine, ref.EndCol
		snap.Iter = ref.IterLabel()
	}
	if f.Kind == flow.FdEndpoint {
		snap.Fd = f.Fd.String()
		if f.Owner != nil {
			snap.Owner = f.Owner.Name()
		} else {
			snap.Owner = "job"
		}
	}
	j.mu.Lock()
	j.byFid[snap.FID] = len(j.graph)
	j.graph = append(j.graph, snap)
	j.mu.Unlock()
}

// onFlowWrite fans a per-flow write out to any watchers of that fid.
func (j *Job) onFlowWrite(fid flow.FID, p []byte) {
	j.mu.Lock()
	lf := j.flowLive[int64(fid)]
	if idx, ok := j.byFid[int64(fid)]; ok {
		j.graph[idx].Bytes += int64(len(p))
		for _, b := range p {
			if b == '\n' {
				j.graph[idx].Lines++
			}
		}
	}
	j.mu.Unlock()
	if lf != nil {
		select {
		case lf.ch <- append([]byte(nil), p...):
		default: // drop for a slow watcher; never block the job
		}
	}
}

// SubscribeFlow opens a live stream of one flow's bytes. The returned channel is
// closed when the job ends. Buffered; drops on overflow.
func (j *Job) SubscribeFlow(fid int64) (<-chan []byte, func()) {
	lf := &liveFlow{fid: fid, ch: make(chan []byte, 256)}
	j.mu.Lock()
	if j.flowLive == nil {
		j.flowLive = map[int64]*liveFlow{}
	}
	j.flowLive[fid] = lf
	j.mu.Unlock()
	cancel := func() {
		j.mu.Lock()
		if j.flowLive[fid] == lf {
			delete(j.flowLive, fid)
		}
		j.mu.Unlock()
	}
	return lf.ch, cancel
}
