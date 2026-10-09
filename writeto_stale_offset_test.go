package pgzip

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
)

// Read drains the current block (z.current = nil) but used to leave z.roff at the
// offset of the earlier partial reads. A following WriteTo (io.Copy) then sliced
// the nil z.current with that stale offset and panicked.
func TestWriteToAfterReadDrainedBlock(t *testing.T) {
	var buf bytes.Buffer
	zw := NewWriter(&buf)
	if _, err := zw.Write(bytes.Repeat([]byte("x"), 3000)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(zr, make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	if _, err := zr.Read(make([]byte, 4096)); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, zr); err != nil || n != 0 {
		t.Fatalf("io.Copy after drain: n=%d err=%v", n, err)
	}
}

// Same bug through archive/tar, which ends right after the end-of-archive marker.
func TestWriteToAfterTarConsumedStream(t *testing.T) {
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	tw.WriteHeader(&tar.Header{Name: "a.txt", Mode: 0o644, Size: 1700})
	tw.Write(bytes.Repeat([]byte("x"), 1700))
	tw.Close()
	var buf bytes.Buffer
	zw := NewWriter(&buf)
	zw.Write(tb.Bytes())
	zw.Close()
	zr, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		if _, err := tr.Next(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, tr)
	}
	if _, err := io.Copy(io.Discard, zr); err != nil {
		t.Fatal(err)
	}
}
