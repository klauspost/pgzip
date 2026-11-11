package pgzip

import (
	"bytes"
	"strings"
	"testing"
)

// shortFooterWriter accepts the gzip stream but reports a partial write
// (with a nil error) for the 8-byte CRC/ISIZE footer. io.Writer forbids
// that combination, but Close must not silently accept it: the resulting
// stream would be truncated without any error to the caller.
type shortFooterWriter struct {
	buf bytes.Buffer
}

func (w *shortFooterWriter) Write(p []byte) (int, error) {
	if len(p) == 8 {
		return w.buf.Write(p[:4])
	}
	return w.buf.Write(p)
}

func TestWriterCloseReportsShortFooterWrite(t *testing.T) {
	w := NewWriter(&shortFooterWriter{})
	if _, err := w.Write([]byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	err := w.Close()
	if err == nil {
		t.Fatal("Close returned nil after a short footer write; truncated gzip stream reported as success")
	}
	if !strings.Contains(err.Error(), "short write") {
		t.Fatalf("expected a short write error, got %v", err)
	}
}
