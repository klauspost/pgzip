// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package zlib

import (
	"bufio"
	"encoding/binary"
	"errors"
	"hash"
	"hash/adler32"
	"io"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/flate"
)

var errClosed = errors.New("zlib: reader closed")

const (
	zlibDeflate   = 8
	zlibMaxWindow = 7
)

func makeReader(r io.Reader) flate.Reader {
	if rr, ok := r.(flate.Reader); ok {
		return rr
	}
	return bufio.NewReader(r)
}

// Resetter resets a ReadCloser returned by NewReader or NewReaderDict
// to switch to a new underlying Reader. This permits reusing a ReadCloser
// instead of allocating a new one.
type Resetter interface {
	// Reset discards any buffered data and resets the Resetter as if it was
	// newly initialized with the given reader.
	Reset(r io.Reader, dict []byte) error
}

type read struct {
	b   []byte
	err error
}

// A Reader is an io.ReadCloser that can be read to retrieve
// uncompressed data from a zlib-format compressed file.
type Reader struct {
	r            flate.Reader
	decompressor io.ReadCloser
	digest       hash.Hash32
	size         uint32
	buf          [512]byte
	err          error
	closeErr     chan error

	readAhead   chan read
	roff        int // read offset
	current     []byte
	closeReader chan struct{}
	lastBlock   bool
	blockSize   int
	blocks      int

	readAheadStarted atomic.Bool
	mu               sync.Mutex // Lock for channels during killReadAhead

	blockPool chan []byte
}

// NewReader creates a new ReadCloser reading the given reader.
// The implementation buffers input and may read more data than necessary from r.
// It is the caller's responsibility to call Close on the ReadCloser when done.
//
// The io.ReadCloser returned by NewReader also implements Resetter.
func NewReader(r io.Reader) (io.ReadCloser, error) {
	return NewReaderDict(r, nil)
}

// NewReaderDict is like NewReader but uses a preset dictionary.
// NewReaderDict ignores the dictionary if the compressed data does not refer to it.
// If the compressed data refers to a different dictionary, NewReaderDict returns ErrDictionary.
//
// The ReadCloser returned by NewReaderDict also implements Resetter.
func NewReaderDict(r io.Reader, dict []byte) (io.ReadCloser, error) {
	z := new(Reader)
	z.blocks = defaultBlocks
	z.blockSize = defaultBlockSize
	z.r = makeReader(r)
	z.digest = adler32.New()
	z.blockPool = make(chan []byte, z.blocks)
	for i := 0; i < z.blocks; i++ {
		z.blockPool <- make([]byte, z.blockSize)
	}
	if err := z.readHeader(dict); err != nil {
		return nil, err
	}
	return z, nil
}

// NewReaderN creates a new Reader reading the given reader with custom block settings.
func NewReaderN(r io.Reader, blockSize, blocks int) (*Reader, error) {
	return NewReaderNDict(r, nil, blockSize, blocks)
}

// NewReaderNDict creates a new Reader reading the given reader with a preset dictionary and custom block settings.
func NewReaderNDict(r io.Reader, dict []byte, blockSize, blocks int) (*Reader, error) {
	z := new(Reader)
	z.blocks = blocks
	z.blockSize = blockSize
	z.r = makeReader(r)
	z.digest = adler32.New()

	if z.blocks <= 0 {
		z.blocks = defaultBlocks
	}
	if z.blockSize <= 512 {
		z.blockSize = defaultBlockSize
	}
	z.blockPool = make(chan []byte, z.blocks)
	for i := 0; i < z.blocks; i++ {
		z.blockPool <- make([]byte, z.blockSize)
	}
	if err := z.readHeader(dict); err != nil {
		return nil, err
	}
	return z, nil
}

func (z *Reader) readHeader(dict []byte) error {
	_, err := io.ReadFull(z.r, z.buf[0:2])
	if err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	h := binary.BigEndian.Uint16(z.buf[0:2])
	if (z.buf[0]&0x0f != zlibDeflate) || (z.buf[0]>>4 > zlibMaxWindow) || (h%31 != 0) {
		return ErrHeader
	}
	haveDict := z.buf[1]&0x20 != 0
	if haveDict {
		_, err = io.ReadFull(z.r, z.buf[0:4])
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return err
		}
		checksum := binary.BigEndian.Uint32(z.buf[0:4])
		if dict == nil || checksum != adler32.Checksum(dict) {
			return ErrDictionary
		}
	}

	z.digest.Reset()
	if z.decompressor == nil {
		if haveDict {
			z.decompressor = flate.NewReaderDict(z.r, dict)
		} else {
			z.decompressor = flate.NewReader(z.r)
		}
	} else {
		if r, ok := z.decompressor.(flate.Resetter); ok {
			if err := r.Reset(z.r, dict); err != nil {
				return err
			}
		} else {
			if haveDict {
				z.decompressor = flate.NewReaderDict(z.r, dict)
			} else {
				z.decompressor = flate.NewReader(z.r)
			}
		}
	}
	return nil
}

func (z *Reader) killReadAhead() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if !z.readAheadStarted.Load() {
		if z.err == nil {
			z.err = errClosed
		}
		return nil
	}

	if z.closeReader != nil {
		close(z.closeReader)
		z.closeReader = nil
	}

	// Wait for decompressor to be closed and return error, if any.
	e, ok := <-z.closeErr

	for blk := range z.readAhead {
		if blk.b != nil {
			z.blockPool <- blk.b
		}
	}
	if cap(z.current) > 0 {
		z.blockPool <- z.current
		z.current = nil
	}
	z.readAheadStarted.Store(false)
	if z.err == nil {
		z.err = errClosed
	}
	if !ok {
		return nil
	}
	return e
}

// Starts readahead.
func (z *Reader) doReadAhead() {
	if z.blocks <= 0 {
		z.blocks = defaultBlocks
	}
	if z.blockSize <= 512 {
		z.blockSize = defaultBlockSize
	}
	ra := make(chan read, z.blocks)
	z.readAhead = ra
	closeReader := make(chan struct{})
	z.closeReader = closeReader
	z.lastBlock = false
	closeErr := make(chan error, 1)
	z.closeErr = closeErr
	z.size = 0
	z.roff = 0
	z.current = nil
	decomp := z.decompressor

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			closeErr <- decomp.Close()
			close(closeErr)
			close(ra)
		}()

		digest := z.digest
		for {
			var buf []byte
			wg.Wait()
			select {
			case buf = <-z.blockPool:
			case <-closeReader:
				return
			}
			buf = buf[0:z.blockSize]
			n, err := io.ReadFull(decomp, buf)
			if err == io.ErrUnexpectedEOF {
				if n > 0 {
					err = nil
				} else {
					_, err = decomp.Read([]byte{})
					if err == io.EOF {
						err = nil
					}
				}
			}
			if n < len(buf) {
				buf = buf[0:n]
			}
			wg.Go(func() {
				digest.Write(buf)
			})
			z.size += uint32(n)

			if err != nil {
				wg.Wait()
			}
			if err == io.EOF {
				// Finished file; check Adler-32 trailer.
				if _, err = io.ReadFull(z.r, z.buf[0:4]); err == nil {
					checksum := binary.BigEndian.Uint32(z.buf[0:4])
					sum := z.digest.Sum32()
					if sum != checksum {
						err = ErrChecksum
					} else {
						err = io.EOF
					}
				} else if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
			}
			if err == nil && len(buf) == 0 {
				z.blockPool <- buf
				continue
			}
			select {
			case z.readAhead <- read{b: buf, err: err}:
			case <-closeReader:
				z.blockPool <- buf
				return
			}
			if err != nil {
				return
			}
		}
	}()
}

// Read reads uncompressed data from the underlying reader.
func (z *Reader) Read(p []byte) (n int, err error) {
	if z.err != nil {
		return 0, z.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if z.lastBlock && len(z.current) == 0 {
		return 0, io.EOF
	}

	if z.readAheadStarted.CompareAndSwap(false, true) {
		z.doReadAhead()
	}

	for {
		if len(z.current) == 0 && !z.lastBlock {
			read := <-z.readAhead
			if read.err != nil {
				z.closeReader = nil

				if read.err != io.EOF {
					if cap(read.b) > 0 {
						z.blockPool <- read.b
					}
					z.err = read.err
					return 0, z.err
				}
				if read.err == io.EOF {
					z.lastBlock = true
					err = nil
				}
			}
			z.current = read.b
			z.roff = 0
		}
		avail := z.current[z.roff:]
		if len(p) >= len(avail) {
			n = copy(p, avail)
			if z.current != nil {
				z.blockPool <- z.current
				z.current = nil
			}
			if z.lastBlock {
				err = io.EOF
				break
			}
		} else {
			n = copy(p, avail)
			z.roff += n
		}
		return n, nil
	}
	return n, err
}

// WriteTo writes uncompressed data directly to w.
func (z *Reader) WriteTo(w io.Writer) (n int64, err error) {
	if z.err != nil {
		return 0, z.err
	}
	if z.readAheadStarted.CompareAndSwap(false, true) {
		z.doReadAhead()
	}

	total := int64(0)
	avail := z.current[z.roff:]
	if len(avail) != 0 {
		written, err := w.Write(avail)
		if written != len(avail) {
			return total, io.ErrShortWrite
		}
		total += int64(written)
		if err != nil {
			return total, err
		}
		z.blockPool <- z.current
		z.current = nil
	}
	for z.err == nil && !z.lastBlock {
		read := <-z.readAhead
		if read.err != nil {
			z.closeReader = nil

			if read.err != io.EOF {
				if cap(read.b) > 0 {
					z.blockPool <- read.b
				}
				z.err = read.err
				return total, z.err
			}
			if read.err == io.EOF {
				z.lastBlock = true
			}
		}
		if len(read.b) > 0 {
			written, err := w.Write(read.b)
			if written != len(read.b) {
				if cap(read.b) > 0 {
					z.blockPool <- read.b
				}
				return total, io.ErrShortWrite
			}
			total += int64(written)
			if err != nil {
				if cap(read.b) > 0 {
					z.blockPool <- read.b
				}
				return total, err
			}
		}
		if cap(read.b) > 0 {
			z.blockPool <- read.b
		}
	}
	if z.err == io.EOF {
		return total, nil
	}
	return total, z.err
}

// Close closes the Reader. It does not close the underlying io.Reader.
func (z *Reader) Close() error {
	return z.killReadAhead()
}

// Reset discards the Reader z's state and makes it equivalent to the
// result of its original state from NewReaderDict, but reading from r instead.
func (z *Reader) Reset(r io.Reader, dict []byte) error {
	_ = z.killReadAhead()
	z.r = makeReader(r)
	z.digest = adler32.New()
	z.size = 0
	z.err = nil
	z.lastBlock = false
	z.current = nil
	z.roff = 0
	z.readAheadStarted.Store(false)

	if z.blocks <= 0 {
		z.blocks = defaultBlocks
	}
	if z.blockSize <= 512 {
		z.blockSize = defaultBlockSize
	}

	if z.blockPool == nil {
		z.blockPool = make(chan []byte, z.blocks)
		for i := 0; i < z.blocks; i++ {
			z.blockPool <- make([]byte, z.blockSize)
		}
	}
	if err := z.readHeader(dict); err != nil {
		return err
	}
	return nil
}
