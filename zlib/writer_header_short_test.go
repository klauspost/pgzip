package zlib

import (
	"bytes"
	"strings"
	"testing"
)

// shortHeaderWriter shortens only the 2-byte header write (len 2). All other
// writes, including the 4-byte checksum and footer, complete normally.
type shortHeaderWriter struct {
	buf bytes.Buffer
}

func (w *shortHeaderWriter) Write(p []byte) (int, error) {
	if len(p) == 2 {
		return w.buf.Write(p[:1])
	}
	return w.buf.Write(p)
}

// shortDictChecksumWriter shortens the 4-byte dictionary checksum but lets the
// 4-byte footer through, so the error can only come from the header path.
type shortDictChecksumWriter struct {
	buf       bytes.Buffer
	dictWrite bool
}

func (w *shortDictChecksumWriter) Write(p []byte) (int, error) {
	if len(p) == 4 && !w.dictWrite {
		w.dictWrite = true
		return w.buf.Write(p[:2])
	}
	return w.buf.Write(p)
}

func TestWriterReportsShortHeaderWrite(t *testing.T) {
	w := NewWriter(&shortHeaderWriter{})
	if err := w.Close(); err == nil {
		t.Fatal("Close returned nil after a short header write")
	} else if !strings.Contains(err.Error(), "short write") {
		t.Fatalf("expected a short write error, got %v", err)
	}
}

func TestWriterReportsShortDictChecksumWrite(t *testing.T) {
	w, err := NewWriterLevelDict(&shortDictChecksumWriter{}, DefaultCompression, []byte("dictionary"))
	if err != nil {
		t.Fatalf("NewWriterLevelDict: %v", err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close returned nil after a short dictionary checksum write")
	} else if !strings.Contains(err.Error(), "short write") {
		t.Fatalf("expected a short write error, got %v", err)
	}
}
