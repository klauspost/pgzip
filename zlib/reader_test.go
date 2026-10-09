// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"bytes"
	stdzlib "compress/zlib"
	"errors"
	"io"
	"testing"
)

func TestReaderRoundTrip(t *testing.T) {
	data := []byte("Testing zlib reader with round trip compression and decompression.")

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("Mismatch: got %q, want %q", out, data)
	}
}

func TestReaderStdlibInteroperability(t *testing.T) {
	data := bytes.Repeat([]byte("Interoperability test between standard library and pgzip zlib! "), 2000)

	// Compress with standard library
	var stdBuf bytes.Buffer
	stdW := stdzlib.NewWriter(&stdBuf)
	if _, err := stdW.Write(data); err != nil {
		t.Fatalf("stdW.Write failed: %v", err)
	}
	if err := stdW.Close(); err != nil {
		t.Fatalf("stdW.Close failed: %v", err)
	}

	// Decompress with our reader
	r, err := NewReader(&stdBuf)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("Decompressed data mismatch from stdlib compressed stream")
	}
}

func TestReaderDict(t *testing.T) {
	dict := []byte("shared dictionary string 12345")
	data := []byte("shared dictionary string 12345 with additional content")

	var buf bytes.Buffer
	w, err := NewWriterLevelDict(&buf, BestCompression, dict)
	if err != nil {
		t.Fatalf("NewWriterLevelDict failed: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read with wrong dictionary
	wrongDict := []byte("wrong dictionary string")
	_, err = NewReaderDict(bytes.NewReader(buf.Bytes()), wrongDict)
	if !errors.Is(err, ErrDictionary) {
		t.Fatalf("expected ErrDictionary with wrong dict, got: %v", err)
	}

	// Read without dictionary
	_, err = NewReader(bytes.NewReader(buf.Bytes()))
	if !errors.Is(err, ErrDictionary) {
		t.Fatalf("expected ErrDictionary without dict, got: %v", err)
	}

	// Read with correct dictionary
	r, err := NewReaderDict(bytes.NewReader(buf.Bytes()), dict)
	if err != nil {
		t.Fatalf("NewReaderDict failed: %v", err)
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("Output mismatch with dictionary")
	}
}

func TestReaderHeaderError(t *testing.T) {
	// Invalid magic bytes
	invalidHeader := []byte{0x12, 0x34, 0x00, 0x00}
	_, err := NewReader(bytes.NewReader(invalidHeader))
	if !errors.Is(err, ErrHeader) {
		t.Fatalf("expected ErrHeader, got: %v", err)
	}
}

func TestReaderChecksumError(t *testing.T) {
	data := []byte("Data for testing trailer checksum validation.")

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	corrupt := buf.Bytes()
	// Corrupt last byte (part of Adler-32 trailer)
	corrupt[len(corrupt)-1] ^= 0xff

	r, err := NewReader(bytes.NewReader(corrupt))
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	_, err = io.ReadAll(r)
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("expected ErrChecksum, got: %v", err)
	}
}

func TestReaderReset(t *testing.T) {
	data1 := []byte("Initial data stream for testing Reader Reset.")
	data2 := []byte("Subsequent data stream to verify Reader Reset reinitialization.")

	var buf1 bytes.Buffer
	w1 := NewWriter(&buf1)
	_, _ = w1.Write(data1)
	_ = w1.Close()

	var buf2 bytes.Buffer
	w2 := NewWriter(&buf2)
	_, _ = w2.Write(data2)
	_ = w2.Close()

	r, err := NewReader(&buf1)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	out1, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll 1 failed: %v", err)
	}
	if !bytes.Equal(out1, data1) {
		t.Fatalf("Mismatch 1")
	}

	resetter, ok := r.(Resetter)
	if !ok {
		t.Fatalf("Reader does not implement Resetter")
	}
	if err := resetter.Reset(&buf2, nil); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	out2, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll 2 failed: %v", err)
	}
	if !bytes.Equal(out2, data2) {
		t.Fatalf("Mismatch 2")
	}
}

func TestReaderWriteTo(t *testing.T) {
	data := bytes.Repeat([]byte("Testing Reader.WriteTo with multi-block data stream. "), 1000)

	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.SetConcurrency(32*1024, 2)
	_, _ = w.Write(data)
	_ = w.Close()

	r, err := NewReaderN(&buf, 16*1024, 4)
	if err != nil {
		t.Fatalf("NewReaderN failed: %v", err)
	}
	defer r.Close()

	var out bytes.Buffer
	n, err := r.WriteTo(&out)
	if err != nil {
		t.Fatalf("WriteTo failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Fatalf("WriteTo count mismatch: got %d, want %d", n, len(data))
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("WriteTo content mismatch")
	}
}

func TestReaderChunked(t *testing.T) {
	data := []byte("Small chunk reads verification pattern.")

	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	var result []byte
	chunk := make([]byte, 7)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			result = append(result, chunk[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	if !bytes.Equal(result, data) {
		t.Fatalf("Chunked read mismatch")
	}
}

func TestReaderCloseTwice(t *testing.T) {
	data := []byte("Testing multiple Close calls on Reader.")
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}

	p := make([]byte, 5)
	n, err := r.Read(p)
	if err != nil || n != 5 {
		t.Fatalf("Read failed: n=%d, err=%v", n, err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	var after [10]byte
	n, err = r.Read(after[:])
	if !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed on Read after Close, got: %v (n=%d)", err, n)
	}
}

func TestReaderCloseThenReset(t *testing.T) {
	data1 := []byte("Stream 1 for close then reset test.")
	data2 := []byte("Stream 2 for close then reset test.")

	var buf1, buf2 bytes.Buffer
	w1 := NewWriter(&buf1)
	_, _ = w1.Write(data1)
	_ = w1.Close()

	w2 := NewWriter(&buf2)
	_, _ = w2.Write(data2)
	_ = w2.Close()

	r, err := NewReader(&buf1)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}

	p := make([]byte, 5)
	_, _ = r.Read(p)

	if err := r.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	resetter, ok := r.(Resetter)
	if !ok {
		t.Fatalf("Reader does not implement Resetter")
	}
	if err := resetter.Reset(&buf2, nil); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll after Reset failed: %v", err)
	}
	if !bytes.Equal(out, data2) {
		t.Fatalf("data mismatch after Reset: got %q, want %q", out, data2)
	}
}

func TestReaderReadAfterClose(t *testing.T) {
	data := []byte("Test read after close.")
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	p := make([]byte, 10)
	n, err := r.Read(p)
	if !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed on Read after Close, got: %v (n=%d)", err, n)
	}

	var out bytes.Buffer
	_, err = r.(*Reader).WriteTo(&out)
	if !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed on WriteTo after Close, got: %v", err)
	}
}

