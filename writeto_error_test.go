package pgzip_test

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"testing"

	pgzip "github.com/klauspost/pgzip"
	pzlib "github.com/klauspost/pgzip/zlib"
)

type failingWriter struct {
	remaining int
	written   int
	err       error
}

func (w *failingWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.remaining)
	w.remaining -= n
	w.written += n
	if n < len(p) {
		return n, w.err
	}
	return n, nil
}

func TestWriteToDestinationError(t *testing.T) {
	payload := bytes.Repeat([]byte("destination error accounting\n"), 300)
	destinationErr := errors.New("destination unavailable")
	for _, format := range []string{"gzip", "zlib"} {
		var compressed bytes.Buffer
		var compressor io.WriteCloser = gzip.NewWriter(&compressed)
		if format == "zlib" {
			compressor = zlib.NewWriter(&compressed)
		}
		if _, err := compressor.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := compressor.Close(); err != nil {
			t.Fatal(err)
		}
		for _, prefix := range []int{0, 17} {
			for _, limit := range []int{0, 23, 1200} {
				for _, sinkErr := range []error{destinationErr, nil} {
					t.Run(fmt.Sprintf("%s/prefix=%d/limit=%d/error=%v", format, prefix, limit, sinkErr), func(t *testing.T) {
						var reader interface {
							io.ReadCloser
							io.WriterTo
						}
						var err error
						if format == "gzip" {
							reader, err = pgzip.NewReaderN(bytes.NewReader(compressed.Bytes()), 1024, 2)
						} else {
							reader, err = pzlib.NewReaderN(bytes.NewReader(compressed.Bytes()), 1024, 2)
						}
						if err != nil {
							t.Fatal(err)
						}
						defer reader.Close()
						if _, err := io.ReadFull(reader, make([]byte, prefix)); err != nil {
							t.Fatal(err)
						}
						writer := &failingWriter{remaining: limit, err: sinkErr}
						n, err := io.Copy(writer, reader)
						if n != int64(writer.written) || n != int64(limit) {
							t.Errorf("copied %d bytes, destination accepted %d, want %d", n, writer.written, limit)
						}
						wantErr := sinkErr
						if wantErr == nil {
							wantErr = io.ErrShortWrite
						}
						if !errors.Is(err, wantErr) {
							t.Errorf("error = %v, want %v", err, wantErr)
						}
					})
				}
			}
		}
	}
}
