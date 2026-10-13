package flow

import (
	"bytes"
	"io"
)

// newBytesReader returns a reader over a fixed byte slice (here-doc bodies).
func newBytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// emptyReader is an immediate-EOF reader (a closed input fd).
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
