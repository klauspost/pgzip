// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pgzip implements reading and writing of gzip format compressed files,
// as specified in RFC 1952.
//
// This is a drop in replacement for "compress/gzip".
// This will split compression into blocks that are compressed in parallel.
// This can be useful for compressing big amounts of data.
// The gzip decompression has not been modified, but remains in the package,
// so you can use it as a complete replacement for "compress/gzip".
//
// See more at https://github.com/klauspost/pgzip
package pgzip

import (
	"bufio"
	"compress/flate"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"sync"
	"time"
)

const (
	gzipID1     = 0x1f
	gzipID2     = 0x8b
	gzipDeflate = 8
	flagText    = 1 << 0
	flagHdrCrc  = 1 << 1
	flagExtra   = 1 << 2
	flagName    = 1 << 3
	flagComment = 1 << 4
)

func makeReader(r io.Reader) flate.Reader {
	if rr, ok := r.(flate.Reader); ok {
		return rr
	}
	return bufio.NewReader(r)
}

var (
	// ErrChecksum is returned when reading GZIP data that has an invalid checksum.
	ErrChecksum = errors.New("gzip: invalid checksum")
	// ErrHeader is returned when reading GZIP data that has an invalid header.
	ErrHeader = errors.New("gzip: invalid header")
)

// The gzip file stores a header giving metadata about the compressed file.
// That header is exposed as the fields of the Writer and Reader structs.
type Header struct {
	Comment string    // comment
	Extra   []byte    // "extra data"
	ModTime time.Time // modification time
	Name    string    // file name
	OS      byte      // operating system type
}

// A Reader is an io.Reader that can be read to retrieve
// uncompressed data from a gzip-format compressed file.
//
// In general, a gzip file can be a concatenation of gzip files,
// each with its own header.  Reads from the Reader
// return the concatenation of the uncompressed data of each.
// Only the first header is recorded in the Reader fields.
//
// Gzip files store a length and checksum of the uncompressed data.
// The Reader will return a ErrChecksum when Read
// reaches the end of the uncompressed data if it does not
// have the expected length or checksum.  Clients should treat data
// returned by Read as tentative until they receive the io.EOF
// marking the end of the data.
type Reader struct {
	Header
	r            flate.Reader
	decompressor io.ReadCloser
	digest       hash.Hash32
	size         uint32
	flg          byte
	buf          [512]byte
	err          error
	closeErr     chan error

	readAhead   chan interface{}
	current     []byte
	closeReader chan struct{}
	lastBlock   bool
	blockSize   int
	blocks      int
}

// NewReader creates a new Reader reading the given reader.
// The implementation buffers input and may read more data than necessary from r.
// It is the caller's responsibility to call Close on the Reader when done.
func NewReader(r io.Reader) (*Reader, error) {
	z := new(Reader)
	z.blocks = defaultBlocks
	z.blockSize = defaultBlockSize
	z.r = makeReader(r)
	z.digest = crc32.NewIEEE()
	if err := z.readHeader(true); err != nil {
		return nil, err
	}
	return z, nil
}

// NewReaderN creates a new Reader reading the given reader.
// The implementation buffers input and may read more data than necessary from r.
// It is the caller's responsibility to call Close on the Reader when done.
//
// With this you can control the approximate size of your blocks,
// as well as how many blocks you want to have prefetched.
//
// Default values for this is blockSize = 250000, blocks = 16,
// meaning up to 16 blocks of maximum 250000 bytes will be
// prefetched.
func NewReaderN(r io.Reader, blockSize, blocks int) (*Reader, error) {
	z := new(Reader)
	z.blocks = blocks
	z.blockSize = blockSize
	z.r = makeReader(r)
	z.digest = crc32.NewIEEE()
	if err := z.readHeader(true); err != nil {
		return nil, err
	}
	return z, nil
}

// Reset discards the Reader z's state and makes it equivalent to the
// result of its original state from NewReader, but reading from r instead.
// This permits reusing a Reader rather than allocating a new one.
func (z *Reader) Reset(r io.Reader) error {
	if z.closeReader != nil {
		close(z.closeReader)
		z.closeReader = nil
	}
	z.r = makeReader(r)
	z.digest = crc32.NewIEEE()
	z.size = 0
	z.err = nil
	return z.readHeader(true)
}

// GZIP (RFC 1952) is little-endian, unlike ZLIB (RFC 1950).
func get4(p []byte) uint32 {
	return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | uint32(p[3])<<24
}

func (z *Reader) readString() (string, error) {
	var err error
	needconv := false
	for i := 0; ; i++ {
		if i >= len(z.buf) {
			return "", ErrHeader
		}
		z.buf[i], err = z.r.ReadByte()
		if err != nil {
			return "", err
		}
		if z.buf[i] > 0x7f {
			needconv = true
		}
		if z.buf[i] == 0 {
			// GZIP (RFC 1952) specifies that strings are NUL-terminated ISO 8859-1 (Latin-1).
			if needconv {
				s := make([]rune, 0, i)
				for _, v := range z.buf[0:i] {
					s = append(s, rune(v))
				}
				return string(s), nil
			}
			return string(z.buf[0:i]), nil
		}
	}
}

func (z *Reader) read2() (uint32, error) {
	_, err := io.ReadFull(z.r, z.buf[0:2])
	if err != nil {
		return 0, err
	}
	return uint32(z.buf[0]) | uint32(z.buf[1])<<8, nil
}

func (z *Reader) readHeader(save bool) error {
	_, err := io.ReadFull(z.r, z.buf[0:10])
	if err != nil {
		return err
	}
	if z.buf[0] != gzipID1 || z.buf[1] != gzipID2 || z.buf[2] != gzipDeflate {
		return ErrHeader
	}
	z.flg = z.buf[3]
	if save {
		z.ModTime = time.Unix(int64(get4(z.buf[4:8])), 0)
		// z.buf[8] is xfl, ignored
		z.OS = z.buf[9]
	}
	z.digest.Reset()
	z.digest.Write(z.buf[0:10])

	if z.flg&flagExtra != 0 {
		n, err := z.read2()
		if err != nil {
			return err
		}
		data := make([]byte, n)
		if _, err = io.ReadFull(z.r, data); err != nil {
			return err
		}
		if save {
			z.Extra = data
		}
	}

	var s string
	if z.flg&flagName != 0 {
		if s, err = z.readString(); err != nil {
			return err
		}
		if save {
			z.Name = s
		}
	}

	if z.flg&flagComment != 0 {
		if s, err = z.readString(); err != nil {
			return err
		}
		if save {
			z.Comment = s
		}
	}

	if z.flg&flagHdrCrc != 0 {
		n, err := z.read2()
		if err != nil {
			return err
		}
		sum := z.digest.Sum32() & 0xFFFF
		if n != sum {
			return ErrHeader
		}
	}

	z.digest.Reset()
	z.decompressor = flate.NewReader(z.r)
	z.doReadAhead()
	return nil
}

// Starts readahead.
// Will return on error (including io.EOF)
// or when z.closeReader is closed.
func (z *Reader) doReadAhead() {
	if z.blocks <= 0 {
		z.blocks = defaultBlocks
	}
	if z.blockSize <= 512 {
		z.blockSize = defaultBlockSize
	}
	z.readAhead = make(chan interface{}, z.blocks*2)
	closeReader := make(chan struct{}, 0)
	z.closeReader = closeReader
	z.lastBlock = false
	closeErr := make(chan error, 1)
	z.closeErr = closeErr

	go func() {
		defer close(z.readAhead)
		defer func() {
			closeErr <- z.decompressor.Close()
			close(closeErr)
		}()

		// We hold a local reference to digest, since
		// it way be changed by reset.
		digest := z.digest
		// Lock for our digest.
		dLock := sync.Mutex{}
		for {
			buf := make([]byte, z.blockSize)
			n, err := z.decompressor.Read(buf)
			if n < len(buf) {
				buf = buf[0:n]
			}

			dLock.Lock()
			go func() {
				digest.Write(buf)
				dLock.Unlock()
			}()
			z.size += uint32(n)

			select {
			case z.readAhead <- buf:
				// Also send the error
				if err != nil {
					// When we send an error, digest must be finished.
					dLock.Lock()
					dLock.Unlock()
				}
				z.readAhead <- err

			case <-closeReader:
				// Sent on close, we don't care about the next results
				return
			}
			if err != nil {
				return
			}
		}
	}()
}

func (z *Reader) Read(p []byte) (n int, err error) {
	if z.err != nil {
		return 0, z.err
	}
	if len(p) == 0 {
		return 0, nil
	}

	for {
		if len(z.current) == 0 && !z.lastBlock {
			bufi := <-z.readAhead
			erri := <-z.readAhead

			if erri != nil {
				// If not nil, the reader will have exited
				z.closeReader = nil

				err = erri.(error)
				if err != io.EOF {
					z.err = err
					return
				}
				if err == io.EOF {
					z.lastBlock = true
					err = nil
				}
			}
			buf := bufi.([]byte)
			z.current = buf
		}
		if len(p) >= len(z.current) {
			// If len(p) >= len(current), return all content of current
			copy(p, z.current)
			n = len(z.current)
			z.current = nil
			if z.lastBlock {
				err = io.EOF
				break
			}
		} else {
			// We copy as much as there is space for, and reslice current
			copy(p, z.current[0:len(p)])
			n = len(p)
			z.current = z.current[n:]
		}
		return
	}

	// Finished file; check checksum + size.
	if _, err := io.ReadFull(z.r, z.buf[0:8]); err != nil {
		z.err = err
		return 0, err
	}
	crc32, isize := get4(z.buf[0:4]), get4(z.buf[4:8])
	sum := z.digest.Sum32()
	if sum != crc32 || isize != z.size {
		z.err = ErrChecksum
		return 0, z.err
	}

	// File is ok; is there another?
	if err = z.readHeader(false); err != nil {
		z.err = err
		return
	}

	// Yes.  Reset and read from it.
	z.digest.Reset()
	z.size = 0
	return z.Read(p)
}

// Close closes the Reader. It does not close the underlying io.Reader.
func (z *Reader) Close() error {
	if z.closeReader != nil {
		close(z.closeReader)
		z.closeReader = nil
	}

	// Wait for decompressor to be closed and return error, if any.
	e, ok := <-z.closeErr
	if !ok {
		// Channel is closed, so if there was any error it has already been returned.
		return nil
	}
	return e
}
