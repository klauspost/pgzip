// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pgzip

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"testing"
	"time"
)

// TestEmpty tests that an empty payload still forms a valid GZIP stream.
func TestEmpty(t *testing.T) {
	buf := new(bytes.Buffer)

	if err := NewWriter(buf).Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	r, err := NewReader(buf)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	b, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(b) != 0 {
		t.Fatalf("got %d bytes, want 0", len(b))
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Reader.Close: %v", err)
	}
}

// TestRoundTrip tests that gzipping and then gunzipping is the identity
// function.
func TestRoundTrip(t *testing.T) {
	buf := new(bytes.Buffer)

	w := NewWriter(buf)
	w.Comment = "comment"
	w.Extra = []byte("extra")
	w.ModTime = time.Unix(1e8, 0)
	w.Name = "name"
	if _, err := w.Write([]byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Writer.Close: %v", err)
	}

	r, err := NewReader(buf)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	b, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(b) != "payload" {
		t.Fatalf("payload is %q, want %q", string(b), "payload")
	}
	if r.Comment != "comment" {
		t.Fatalf("comment is %q, want %q", r.Comment, "comment")
	}
	if string(r.Extra) != "extra" {
		t.Fatalf("extra is %q, want %q", r.Extra, "extra")
	}
	if r.ModTime.Unix() != 1e8 {
		t.Fatalf("mtime is %d, want %d", r.ModTime.Unix(), uint32(1e8))
	}
	if r.Name != "name" {
		t.Fatalf("name is %q, want %q", r.Name, "name")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Reader.Close: %v", err)
	}
}

// TestLatin1 tests the internal functions for converting to and from Latin-1.
func TestLatin1(t *testing.T) {
	latin1 := []byte{0xc4, 'u', 0xdf, 'e', 'r', 'u', 'n', 'g', 0}
	utf8 := "Äußerung"
	z := Reader{r: bufio.NewReader(bytes.NewReader(latin1))}
	s, err := z.readString()
	if err != nil {
		t.Fatalf("readString: %v", err)
	}
	if s != utf8 {
		t.Fatalf("read latin-1: got %q, want %q", s, utf8)
	}

	buf := bytes.NewBuffer(make([]byte, 0, len(latin1)))
	c := Writer{w: buf}
	if err = c.writeString(utf8); err != nil {
		t.Fatalf("writeString: %v", err)
	}
	s = buf.String()
	if s != string(latin1) {
		t.Fatalf("write utf-8: got %q, want %q", s, string(latin1))
	}
}

// TestLatin1RoundTrip tests that metadata that is representable in Latin-1
// survives a round trip.
func TestLatin1RoundTrip(t *testing.T) {
	testCases := []struct {
		name string
		ok   bool
	}{
		{"", true},
		{"ASCII is OK", true},
		{"unless it contains a NUL\x00", false},
		{"no matter where \x00 occurs", false},
		{"\x00\x00\x00", false},
		{"Látin-1 also passes (U+00E1)", true},
		{"but LĀtin Extended-A (U+0100) does not", false},
		{"neither does 日本語", false},
		{"invalid UTF-8 also \xffails", false},
		{"\x00 as does Látin-1 with NUL", false},
	}
	for _, tc := range testCases {
		buf := new(bytes.Buffer)

		w := NewWriter(buf)
		w.Name = tc.name
		err := w.Close()
		if (err == nil) != tc.ok {
			t.Errorf("Writer.Close: name = %q, err = %v", tc.name, err)
			continue
		}
		if !tc.ok {
			continue
		}

		r, err := NewReader(buf)
		if err != nil {
			t.Errorf("NewReader: %v", err)
			continue
		}
		_, err = ioutil.ReadAll(r)
		if err != nil {
			t.Errorf("ReadAll: %v", err)
			continue
		}
		if r.Name != tc.name {
			t.Errorf("name is %q, want %q", r.Name, tc.name)
			continue
		}
		if err := r.Close(); err != nil {
			t.Errorf("Reader.Close: %v", err)
			continue
		}
	}
}

func TestWriterFlush(t *testing.T) {
	buf := new(bytes.Buffer)

	w := NewWriter(buf)
	w.Comment = "comment"
	w.Extra = []byte("extra")
	w.ModTime = time.Unix(1e8, 0)
	w.Name = "name"

	n0 := buf.Len()
	if n0 != 0 {
		t.Fatalf("buffer size = %d before writes; want 0", n0)
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	n1 := buf.Len()
	if n1 == 0 {
		t.Fatal("no data after first flush")
	}

	w.Write([]byte("x"))

	n2 := buf.Len()
	if n1 != n2 {
		t.Fatalf("after writing a single byte, size changed from %d to %d; want no change", n1, n2)
	}

	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	n3 := buf.Len()
	if n2 == n3 {
		t.Fatal("Flush didn't flush any data")
	}
}

// Multiple gzip files concatenated form a valid gzip file.
func TestConcat(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Write([]byte("hello "))
	w.Close()
	w = NewWriter(&buf)
	w.Write([]byte("world\n"))
	w.Close()

	r, err := NewReader(&buf)
	data, err := ioutil.ReadAll(r)
	if string(data) != "hello world\n" || err != nil {
		t.Fatalf("ReadAll = %q, %v, want %q, nil", data, err, "hello world")
	}
}

func TestWriterReset(t *testing.T) {
	buf := new(bytes.Buffer)
	buf2 := new(bytes.Buffer)
	z := NewWriter(buf)
	msg := []byte("hello world")
	z.Write(msg)
	z.Close()
	z.Reset(buf2)
	z.Write(msg)
	z.Close()
	if buf.String() != buf2.String() {
		t.Errorf("buf2 %q != original buf of %q", buf2.String(), buf.String())
	}
}

var testbuf []byte

func testFile(i int, t *testing.T) {
	dat, _ := ioutil.ReadFile("testdata/test.json")
	dl := len(dat)
	if len(testbuf) != i*dl {
		// Make results predictable
		testbuf = make([]byte, i*dl)
		for j := 0; j < i; j++ {
			copy(testbuf[j*dl:j*dl+dl], dat)
		}
	}

	br := bytes.NewBuffer(testbuf)
	var buf bytes.Buffer
	w, _ := NewWriterLevel(&buf, 6)
	io.Copy(w, br)
	w.Close()
	/*
		fo, err := os.Create(fmt.Sprintf("testfile.%d.gz", i))
		_, _ = fo.Write(buf.Bytes())
		fo.Close()
	*/
	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err.Error())
	}
	decoded, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatal(err.Error())
	}
	if !bytes.Equal(testbuf, decoded) {
		t.Errorf("decoded content does not match.")
	}
}

func TestFile1(b *testing.T)   { testFile(1, b) }
func TestFile10(b *testing.T)  { testFile(10, b) }
func TestFile50(b *testing.T)  { testFile(50, b) }
func TestFile200(b *testing.T) { testFile(200, b) }

func testBigGzip(i int, t *testing.T) {
	if len(testbuf) != i {
		// Make results predictable
		rand.Seed(1337)
		testbuf = make([]byte, i)
		for idx := range testbuf {
			testbuf[idx] = byte(65 + rand.Intn(32))
		}
	}

	br := bytes.NewBuffer(testbuf)
	var buf bytes.Buffer
	w, _ := NewWriterLevel(&buf, 6)
	io.Copy(w, br)
	// Test UncompressedSize()
	if len(testbuf) != w.UncompressedSize() {
		t.Errorf("uncompressed size does not match. buffer:%d, UncompressedSize():%d", len(testbuf), w.UncompressedSize())
	}
	w.Close()
	// Close should not affect the number
	if len(testbuf) != w.UncompressedSize() {
		t.Errorf("uncompressed size does not match. buffer:%d, UncompressedSize():%d", len(testbuf), w.UncompressedSize())
	}
	/*	fo, err := os.Create(fmt.Sprintf("output.%d.gz", i))
		_, _ = fo.Write(buf.Bytes())
		fo.Close()


		fo, err = os.Create(fmt.Sprintf("output.%d", i))
			_, _ = fo.Write(testbuf)
			fo.Close()
	*/
	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err.Error())
	}
	decoded, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatal(err.Error())
	}
	if !bytes.Equal(testbuf, decoded) {
		t.Errorf("decoded content does not match.")
	}
}

func TestGzip1K(b *testing.T)   { testBigGzip(1000, b) }
func TestGzip100K(b *testing.T) { testBigGzip(100000, b) }
func TestGzip1M(b *testing.T)   { testBigGzip(1000000, b) }
func TestGzip10M(b *testing.T)  { testBigGzip(10000000, b) }

//func TestGzip30M(b *testing.T)  { testBigGzip(30000000, b) }

func benchmarkGzip(i int, b *testing.B) {
	if len(testbuf) != i {
		b.StopTimer()
		rand.Seed(1337)
		testbuf = make([]byte, i)
		for idx := range testbuf {
			testbuf[idx] = byte(65 + rand.Intn(32))
		}
		b.StartTimer()
	}
	t := time.Now()
	n := 0
	for ; n < b.N; n++ {
		br := bytes.NewBuffer(testbuf)
		buf := new(bytes.Buffer)
		w, _ := NewWriterLevel(buf, 6)
		io.Copy(w, br)
		w.Close()
	}
	delta := time.Now().Sub(t)
	s := delta.Seconds()
	fmt.Printf("%fMB/s\n", (float64(n*i)/1000000.0)/s)
}

func BenchmarkGzip1M(b *testing.B)   { benchmarkGzip(1000000, b) }
func BenchmarkGzip10M(b *testing.B)  { benchmarkGzip(10000000, b) }
func BenchmarkGzip30M(b *testing.B)  { benchmarkGzip(30000000, b) }
func BenchmarkGzip100M(b *testing.B) { benchmarkGzip(100000000, b) }
