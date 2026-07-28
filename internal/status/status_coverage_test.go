package status

import (
	"errors"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestWriteReportsWriterFailure(t *testing.T) {
	t.Parallel()

	if err := Write(failingWriter{}, Snapshot{}); err == nil {
		t.Fatal("Write() error = nil, want writer failure")
	}
}
