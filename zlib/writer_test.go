// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"bytes"
	stdzlib "compress/zlib"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"
)

func TestWriterEmpty(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify using standard library reader
	r, err := stdzlib.NewReader(&buf)
	if err != nil {
		t.Fatalf("stdzlib.NewReader failed: %v", err)
	}
	defer r.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 bytes, got %d", len(out))
	}
	if sz := w.UncompressedSize(); sz != 0 {
		t.Fatalf("expected UncompressedSize 0, got %d", sz)
	}
}

func TestWriterLevels(t *testing.T) {
	sample := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. 1234567890\n"), 1000)

	levels := []int{
		NoCompression,
		BestSpeed,
		BestCompression,
		DefaultCompression,
		HuffmanOnly,
		ConstantCompression,
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9,
	}

	for _, lvl := range levels {
		t.Run("level-"+strconv.Itoa(lvl), func(t *testing.T) {
			var buf bytes.Buffer
			w, err := NewWriterLevel(&buf, lvl)
			if err != nil {
				t.Fatalf("NewWriterLevel(%d) failed: %v", lvl, err)
			}

			n, err := w.Write(sample)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}
			if n != len(sample) {
				t.Fatalf("Write length mismatch: expected %d, got %d", len(sample), n)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}

			if w.UncompressedSize() != len(sample) {
				t.Fatalf("UncompressedSize mismatch: expected %d, got %d", len(sample), w.UncompressedSize())
			}

			// Decompress with Go standard library
			r, err := stdzlib.NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("stdzlib.NewReader failed: %v", err)
			}
			decompressed, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll failed: %v", err)
			}
			if !bytes.Equal(decompressed, sample) {
				t.Fatalf("Decompressed content mismatch")
			}
		})
	}
}

func TestWriterConcurrency(t *testing.T) {
	// 5 MB of repeating compressible data to span multiple blocks
	payload := make([]byte, 5*1024*1024)
	pattern := []byte("Concurrent compression testing pattern with pgzip zlib! ")
	for i := 0; i < len(payload); i += len(pattern) {
		copy(payload[i:], pattern)
	}

	configs := []struct {
		blockSize int
		blocks    int
	}{
		{blockSize: 32 * 1024, blocks: 2},
		{blockSize: 64 * 1024, blocks: 4},
		{blockSize: 128 * 1024, blocks: 8},
		{blockSize: 256 * 1024, blocks: 4},
		{blockSize: 1024 * 1024, blocks: 2},
	}

	for _, cfg := range configs {
		name := fmt.Sprintf("bs-%d-blocks-%d", cfg.blockSize, cfg.blocks)
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			if err := w.SetConcurrency(cfg.blockSize, cfg.blocks); err != nil {
				t.Fatalf("SetConcurrency failed: %v", err)
			}

			// Write in smaller chunks
			chunkSize := 47 * 1024
			for i := 0; i < len(payload); i += chunkSize {
				end := i + chunkSize
				if end > len(payload) {
					end = len(payload)
				}
				n, err := w.Write(payload[i:end])
				if err != nil {
					t.Fatalf("Write chunk failed at %d: %v", i, err)
				}
				if n != end-i {
					t.Fatalf("Short write at %d: expected %d, got %d", i, end-i, n)
				}
			}

			if err := w.Close(); err != nil {
				t.Fatalf("Close failed: %v", err)
			}

			// Decompress with stdlib
			r, err := stdzlib.NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("stdzlib.NewReader failed: %v", err)
			}
			decompressed, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll failed: %v", err)
			}
			if !bytes.Equal(decompressed, payload) {
				t.Fatalf("Decompressed content mismatch")
			}
		})
	}
}

func TestWriterDictionary(t *testing.T) {
	dict := []byte("custom preset compression dictionary string for tests")
	input := []byte("custom preset compression dictionary string for tests that repeats custom preset compression dictionary")

	var bufWithDict bytes.Buffer
	wWithDict, err := NewWriterLevelDict(&bufWithDict, BestCompression, dict)
	if err != nil {
		t.Fatalf("NewWriterLevelDict failed: %v", err)
	}
	if _, err := wWithDict.Write(input); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := wWithDict.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	var bufWithoutDict bytes.Buffer
	wWithoutDict, err := NewWriterLevelDict(&bufWithoutDict, BestCompression, nil)
	if err != nil {
		t.Fatalf("NewWriterLevelDict without dict failed: %v", err)
	}
	if _, err := wWithoutDict.Write(input); err != nil {
		t.Fatalf("Write without dict failed: %v", err)
	}
	if err := wWithoutDict.Close(); err != nil {
		t.Fatalf("Close without dict failed: %v", err)
	}

	// Decompressing with wrong dict should fail
	wrongDict := []byte("incorrect dictionary")
	_, err = stdzlib.NewReaderDict(bytes.NewReader(bufWithDict.Bytes()), wrongDict)
	if !errors.Is(err, stdzlib.ErrDictionary) && err == nil {
		t.Fatalf("expected ErrDictionary with wrong dict, got: %v", err)
	}

	// Decompressing without dict should fail
	_, err = stdzlib.NewReader(bytes.NewReader(bufWithDict.Bytes()))
	if !errors.Is(err, stdzlib.ErrDictionary) && err == nil {
		t.Fatalf("expected ErrDictionary with nil dict, got: %v", err)
	}

	// Decompressing with correct dict should succeed
	r, err := stdzlib.NewReaderDict(bytes.NewReader(bufWithDict.Bytes()), dict)
	if err != nil {
		t.Fatalf("stdzlib.NewReaderDict failed: %v", err)
	}
	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(decompressed, input) {
		t.Fatalf("Decompressed mismatch with dict")
	}

	// Dictionary compression should yield smaller output for dictionary-matching input
	if bufWithDict.Len() >= bufWithoutDict.Len() {
		t.Logf("dict size: %d, non-dict size: %d", bufWithDict.Len(), bufWithoutDict.Len())
	}
}

func TestWriterReset(t *testing.T) {
	data1 := []byte("First dataset for testing Writer Reset functionality.")
	data2 := []byte("Second completely distinct dataset for testing Writer Reset reuse.")

	var buf1 bytes.Buffer
	w := NewWriter(&buf1)
	if _, err := w.Write(data1); err != nil {
		t.Fatalf("Write 1 failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 1 failed: %v", err)
	}

	var buf2 bytes.Buffer
	w.Reset(&buf2)
	if _, err := w.Write(data2); err != nil {
		t.Fatalf("Write 2 failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 2 failed: %v", err)
	}

	// Verify buf1
	r1, err := stdzlib.NewReader(&buf1)
	if err != nil {
		t.Fatalf("NewReader 1 failed: %v", err)
	}
	out1, err := io.ReadAll(r1)
	if err != nil {
		t.Fatalf("ReadAll 1 failed: %v", err)
	}
	if !bytes.Equal(out1, data1) {
		t.Fatalf("Output 1 mismatch")
	}

	// Verify buf2
	r2, err := stdzlib.NewReader(&buf2)
	if err != nil {
		t.Fatalf("NewReader 2 failed: %v", err)
	}
	out2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll 2 failed: %v", err)
	}
	if !bytes.Equal(out2, data2) {
		t.Fatalf("Output 2 mismatch")
	}
}

func TestWriterFlush(t *testing.T) {
	pipeR, pipeW := io.Pipe()
	w := NewWriter(pipeW)

	chunk1 := []byte("Initial packet chunk before flush. ")
	chunk2 := []byte("Follow-up packet chunk after flush.")

	errCh := make(chan error, 1)
	go func() {
		defer pipeW.Close()
		defer w.Close()

		if _, err := w.Write(chunk1); err != nil {
			errCh <- err
			return
		}
		if err := w.Flush(); err != nil {
			errCh <- err
			return
		}

		if _, err := w.Write(chunk2); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	r, err := stdzlib.NewReader(pipeR)
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer r.Close()

	buf := make([]byte, len(chunk1))
	n, err := io.ReadFull(r, buf)
	if err != nil {
		t.Fatalf("ReadFull chunk1 failed: %v", err)
	}
	if !bytes.Equal(buf[:n], chunk1) {
		t.Fatalf("chunk1 mismatch: got %q, want %q", buf[:n], chunk1)
	}

	remaining, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll remaining failed: %v", err)
	}
	if !bytes.Equal(remaining, chunk2) {
		t.Fatalf("chunk2 mismatch: got %q, want %q", remaining, chunk2)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Writer goroutine failed: %v", err)
	}
}

func TestWriterInvalidLevelsAndParameters(t *testing.T) {
	var buf bytes.Buffer

	// Invalid compression levels
	if _, err := NewWriterLevel(&buf, -3); err == nil {
		t.Errorf("expected error for level -3, got nil")
	}
	if _, err := NewWriterLevel(&buf, 10); err == nil {
		t.Errorf("expected error for level 10, got nil")
	}

	w := NewWriter(&buf)
	// Invalid concurrency
	if err := w.SetConcurrency(100, 4); err == nil {
		t.Errorf("expected error for blockSize <= tailSize, got nil")
	}
	if err := w.SetConcurrency(100000, 0); err == nil {
		t.Errorf("expected error for blocks <= 0, got nil")
	}
	if err := w.SetConcurrency(100000, -1); err == nil {
		t.Errorf("expected error for blocks < 0, got nil")
	}

	// Setting concurrency after write starts
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.SetConcurrency(100000, 2); err == nil {
		t.Errorf("expected error for SetConcurrency after write, got nil")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Write after close
	if _, err := w.Write([]byte("more")); err == nil {
		t.Errorf("expected error for Write after Close, got nil")
	}
	// Flush after close should be no-op
	if err := w.Flush(); err != nil {
		t.Errorf("Flush after Close should return nil, got: %v", err)
	}
	// Close after close should be no-op
	if err := w.Close(); err != nil {
		t.Errorf("Close after Close should return nil, got: %v", err)
	}
}

type failWriter struct {
	failAfter int
	written   int
}

func (fw *failWriter) Write(p []byte) (int, error) {
	fw.written += len(p)
	if fw.written >= fw.failAfter {
		return 0, errors.New("simulated underlying write error")
	}
	return len(p), nil
}

func TestWriterErrorPropagation(t *testing.T) {
	fw := &failWriter{failAfter: 50}
	w := NewWriter(fw)
	_ = w.SetConcurrency(32*1024, 2)

	payload := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz"), 10000)
	_, err := w.Write(payload)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		t.Fatalf("expected error from failWriter, got nil")
	}
}

func TestWriterRandomDataRoundTrip(t *testing.T) {
	payload := make([]byte, 1024*1024)
	if _, err := io.ReadFull(rand.Reader, payload); err != nil {
		t.Fatalf("ReadFull random data failed: %v", err)
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.SetConcurrency(64*1024, 4); err != nil {
		t.Fatalf("SetConcurrency failed: %v", err)
	}

	if _, err := w.Write(payload); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	r, err := stdzlib.NewReader(&buf)
	if err != nil {
		t.Fatalf("stdzlib.NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if !bytes.Equal(decompressed, payload) {
		t.Fatalf("Random payload roundtrip mismatch")
	}
}
