// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package zlib implements reading and writing of zlib format compressed data,
// as specified in RFC 1950.
//
// This is a parallel drop-in replacement for "compress/zlib".
// Writes to a Writer are split into blocks and compressed in parallel using DEFLATE.
package zlib

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/adler32"
	"io"
	"runtime"
	"sync"

	"github.com/klauspost/compress/flate"
)

const (
	defaultBlockSize = 1 << 20
	tailSize         = 16384
	defaultBlocks    = 4
)

// These constants are copied from the flate package, so that code that imports
// "compress/zlib" does not also have to import "compress/flate".
const (
	NoCompression       = flate.NoCompression
	BestSpeed           = flate.BestSpeed
	BestCompression     = flate.BestCompression
	DefaultCompression  = flate.DefaultCompression
	ConstantCompression = flate.ConstantCompression
	HuffmanOnly         = flate.HuffmanOnly
)

var (
	// ErrChecksum is returned when reading ZLIB data that has an invalid checksum.
	ErrChecksum = errors.New("zlib: invalid checksum")
	// ErrDictionary is returned when reading ZLIB data that has an invalid dictionary.
	ErrDictionary = errors.New("zlib: invalid dictionary")
	// ErrHeader is returned when reading ZLIB data that has an invalid header.
	ErrHeader = errors.New("zlib: invalid header")
)

// A Writer is an io.WriteCloser.
// Writes to a Writer are compressed and written to w.
type Writer struct {
	w                io.Writer
	level            int
	dict             []byte
	wroteHeader      bool
	blockSize        int
	blocks           int
	currentBuffer    []byte
	prevTail         []byte
	prevTailIsPooled bool
	digest           hash.Hash32
	size             int
	closed           bool
	scratch          [4]byte
	errMu            sync.RWMutex
	err              error
	pushedErr        chan struct{}
	results          chan result
	dictFlatePool    sync.Pool
	dstPool          sync.Pool
	wg               sync.WaitGroup
}

type result struct {
	result        chan []byte
	notifyWritten chan struct{}
}

// SetConcurrency can finetune the concurrency level if needed.
//
// With this you can control the approximate size of your blocks,
// as well as how many you want to be processing in parallel.
//
// Default values for this is SetConcurrency(defaultBlockSize, runtime.GOMAXPROCS(0)),
// meaning blocks are split at 1 MB and up to the number of CPU threads
// can be processing at once before the writer blocks.
func (z *Writer) SetConcurrency(blockSize, blocks int) error {
	if blockSize <= tailSize {
		return fmt.Errorf("zlib: block size cannot be less than or equal to %d", tailSize)
	}
	if blocks <= 0 {
		return errors.New("zlib: blocks cannot be zero or less")
	}
	if z.wroteHeader {
		return errors.New("zlib: cannot set concurrency after write has started")
	}
	if blockSize == z.blockSize && blocks == z.blocks {
		return nil
	}
	z.blockSize = blockSize
	z.results = make(chan result, blocks)
	z.blocks = blocks
	z.dstPool.New = func() any { return make([]byte, 0, blockSize+(blockSize>>4)) }
	return nil
}

// NewWriter creates a new Writer.
// Writes to the returned Writer are compressed and written to w.
//
// It is the caller's responsibility to call Close on the Writer when done.
// Writes may be buffered and not flushed until Close.
func NewWriter(w io.Writer) *Writer {
	z, _ := NewWriterLevelDict(w, DefaultCompression, nil)
	return z
}

// NewWriterLevel is like NewWriter but specifies the compression level instead
// of assuming DefaultCompression.
//
// The compression level can be DefaultCompression, NoCompression, HuffmanOnly
// or any integer value between BestSpeed and BestCompression inclusive.
// The error returned will be nil if the level is valid.
func NewWriterLevel(w io.Writer, level int) (*Writer, error) {
	return NewWriterLevelDict(w, level, nil)
}

// NewWriterLevelDict is like NewWriterLevel but specifies a dictionary to
// compress with.
//
// The dictionary may be nil. If not, its contents should not be modified until
// the Writer is closed.
func NewWriterLevelDict(w io.Writer, level int, dict []byte) (*Writer, error) {
	if level < HuffmanOnly || level > BestCompression {
		return nil, fmt.Errorf("zlib: invalid compression level: %d", level)
	}
	z := new(Writer)
	_ = z.SetConcurrency(defaultBlockSize, runtime.GOMAXPROCS(0))
	z.init(w, level, dict)
	return z, nil
}

// pushError sets an error condition on the writer safely across goroutines.
func (z *Writer) pushError(err error) {
	z.errMu.Lock()
	if z.err != nil {
		z.errMu.Unlock()
		return
	}
	z.err = err
	close(z.pushedErr)
	z.errMu.Unlock()
}

// checkError returns an error if it has been set.
func (z *Writer) checkError() error {
	z.errMu.RLock()
	err := z.err
	z.errMu.RUnlock()
	return err
}

func (z *Writer) init(w io.Writer, level int, dict []byte) {
	z.wg.Wait()
	digest := z.digest
	if digest != nil {
		digest.Reset()
	} else {
		digest = adler32.New()
	}
	z.w = w
	z.level = level
	z.dict = dict
	z.digest = digest
	z.pushedErr = make(chan struct{})
	z.results = make(chan result, z.blocks)
	z.err = nil
	z.closed = false
	z.wroteHeader = false
	if z.currentBuffer != nil {
		z.dstPool.Put(z.currentBuffer)
		z.currentBuffer = nil
	}
	z.scratch = [4]byte{}
	if z.prevTailIsPooled && z.prevTail != nil {
		z.dstPool.Put(z.prevTail)
	}
	z.prevTail = dict
	z.prevTailIsPooled = false
	z.size = 0
	if z.dictFlatePool.New == nil {
		z.dictFlatePool.New = func() any {
			f, _ := flate.NewWriterDict(w, level, nil)
			return f
		}
	}
}

// Reset clears the state of the Writer z such that it is equivalent to its
// initial state from NewWriterLevel or NewWriterLevelDict, but instead writing
// to w.
func (z *Writer) Reset(w io.Writer) {
	if z.results != nil && !z.closed {
		close(z.results)
	}
	_ = z.SetConcurrency(defaultBlockSize, runtime.GOMAXPROCS(0))
	z.init(w, z.level, z.dict)
}

// writeHeader writes the ZLIB header (RFC 1950).
func (z *Writer) writeHeader() error {
	z.wroteHeader = true
	// ZLIB has a two-byte header (RFC 1950).
	// CINFO: 7 (32K window size), CM: 8 (deflate).
	z.scratch[0] = 0x78
	// FLEVEL (compression level): 0=fastest, 1=fast, 2=default, 3=best.
	// FDICT: set if a dictionary is given.
	// FCHECK: mod-31 checksum.
	switch z.level {
	case -2, 0, 1:
		z.scratch[1] = 0 << 6
	case 2, 3, 4, 5:
		z.scratch[1] = 1 << 6
	case 6, -1:
		z.scratch[1] = 2 << 6
	case 7, 8, 9:
		z.scratch[1] = 3 << 6
	default:
		z.scratch[1] = 0 << 6
	}
	if z.dict != nil {
		z.scratch[1] |= 1 << 5
	}
	z.scratch[1] += uint8(31 - binary.BigEndian.Uint16(z.scratch[:2])%31)
	if _, err := z.w.Write(z.scratch[0:2]); err != nil {
		z.pushError(err)
		return err
	}
	if z.dict != nil {
		// The next four bytes are the Adler-32 checksum of the dictionary.
		binary.BigEndian.PutUint32(z.scratch[:], adler32.Checksum(z.dict))
		if _, err := z.w.Write(z.scratch[0:4]); err != nil {
			z.pushError(err)
			return err
		}
	}

	// Start receiving data from compressors
	go func() {
		listen := z.results
		var failed bool
		for {
			r, ok := <-listen
			if !ok {
				return
			}
			if failed {
				close(r.notifyWritten)
				continue
			}
			buf := <-r.result
			n, err := z.w.Write(buf)
			if err != nil {
				z.pushError(err)
				close(r.notifyWritten)
				failed = true
				continue
			}
			if n != len(buf) {
				z.pushError(fmt.Errorf("zlib: short write %d should be %d", n, len(buf)))
				failed = true
				close(r.notifyWritten)
				continue
			}
			z.dstPool.Put(buf)
			close(r.notifyWritten)
		}
	}()

	z.currentBuffer = z.dstPool.Get().([]byte)
	z.currentBuffer = z.currentBuffer[:0]
	return nil
}

// compressCurrent compresses the data currently buffered.
func (z *Writer) compressCurrent(flush bool) {
	c := z.currentBuffer
	if len(c) > z.blockSize {
		panic("len(z.currentBuffer) > z.blockSize (most likely due to concurrent Write race)")
	}

	r := result{}
	r.result = make(chan []byte, 1)
	r.notifyWritten = make(chan struct{})
	// Reserve a result slot
	select {
	case z.results <- r:
	case <-z.pushedErr:
		return
	}

	z.wg.Add(1)
	tail := z.prevTail
	pooledTail := z.prevTailIsPooled
	if len(c) > tailSize {
		buf := z.dstPool.Get().([]byte)
		buf = append(buf[:0], c[len(c)-tailSize:]...)
		z.prevTail = buf
		z.prevTailIsPooled = true
	} else {
		z.prevTail = nil
		z.prevTailIsPooled = false
	}
	go z.compressBlock(c, tail, r, z.closed, pooledTail)

	z.currentBuffer = z.dstPool.Get().([]byte)
	z.currentBuffer = z.currentBuffer[:0]

	// Wait if flushing
	if flush {
		<-r.notifyWritten
	}
}

// Write writes a compressed form of p to the underlying io.Writer. The
// compressed bytes are not necessarily flushed to output until
// the Writer is closed or Flush() is called.
func (z *Writer) Write(p []byte) (int, error) {
	if err := z.checkError(); err != nil {
		return 0, err
	}
	if z.closed {
		return 0, errors.New("zlib: write after close")
	}
	if !z.wroteHeader {
		if err := z.writeHeader(); err != nil {
			return 0, err
		}
	}
	q := p
	for len(q) > 0 {
		length := len(q)
		if length+len(z.currentBuffer) > z.blockSize {
			length = z.blockSize - len(z.currentBuffer)
		}
		z.digest.Write(q[:length])
		z.currentBuffer = append(z.currentBuffer, q[:length]...)
		if len(z.currentBuffer) > z.blockSize {
			panic("z.currentBuffer too large (most likely due to concurrent Write race)")
		}
		if len(z.currentBuffer) == z.blockSize {
			z.compressCurrent(false)
			if err := z.checkError(); err != nil {
				return len(p) - len(q), err
			}
		}
		z.size += length
		q = q[length:]
	}
	return len(p), z.checkError()
}

// compressBlock compresses buffer p using flate and sends result to r.
func (z *Writer) compressBlock(p, prevTail []byte, r result, closed bool, pooledTail bool) {
	defer func() {
		close(r.result)
		z.wg.Done()
	}()
	buf := z.dstPool.Get().([]byte)
	dest := bytes.NewBuffer(buf[:0])

	compressor := z.dictFlatePool.Get().(*flate.Writer)
	compressor.ResetDict(dest, prevTail)
	compressor.Write(p)
	z.dstPool.Put(p)

	err := compressor.Flush()
	if err != nil {
		z.pushError(err)
		return
	}
	if closed {
		err = compressor.Close()
		if err != nil {
			z.pushError(err)
			return
		}
	}
	z.dictFlatePool.Put(compressor)

	if pooledTail && prevTail != nil {
		z.dstPool.Put(prevTail)
	}

	buf = dest.Bytes()
	r.result <- buf
}

// Flush flushes any pending compressed data to the underlying writer.
//
// In the terminology of the zlib library, Flush is equivalent to Z_SYNC_FLUSH.
func (z *Writer) Flush() error {
	if err := z.checkError(); err != nil {
		return err
	}
	if z.closed {
		return nil
	}
	if !z.wroteHeader {
		if err := z.writeHeader(); err != nil {
			return err
		}
	}
	z.compressCurrent(true)
	return z.checkError()
}

// UncompressedSize will return the number of bytes written.
func (z *Writer) UncompressedSize() int {
	return z.size
}

// Close closes the Writer, flushing any unwritten data to the underlying
// io.Writer, but does not close the underlying io.Writer.
func (z *Writer) Close() error {
	if err := z.checkError(); err != nil {
		return err
	}
	if z.closed {
		return nil
	}

	z.closed = true
	if !z.wroteHeader {
		if err := z.writeHeader(); err != nil {
			return err
		}
	}
	z.compressCurrent(true)
	if err := z.checkError(); err != nil {
		return err
	}
	close(z.results)
	binary.BigEndian.PutUint32(z.scratch[:], z.digest.Sum32())
	_, err := z.w.Write(z.scratch[0:4])
	if err != nil {
		z.pushError(err)
		return err
	}
	return nil
}
