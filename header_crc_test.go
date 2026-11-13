package pgzip

import (
	"bytes"
	"compress/gzip"
	"hash/crc32"
	"io"
	"testing"
)

// headerCRCStream adds RFC 1952's optional fields and FHCRC to a standard
// library stream. The standard writer does not itself emit FHCRC.
func headerCRCStream(t *testing.T, flags byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	header := append([]byte(nil), compressed.Bytes()[:10]...)
	header[3] = flags | flagHdrCrc
	if flags&flagExtra != 0 {
		header = append(header, 3, 0, 0, 0xff, 42)
	}
	if flags&flagName != 0 {
		header = append(header, 'n', 0xe4, 'm', 'e', 0)
	}
	if flags&flagComment != 0 {
		header = append(header, 'c', 0xf6, 'm', 'm', 'e', 'n', 't', 0)
	}
	checksum := crc32.ChecksumIEEE(header)
	header = append(header, byte(checksum), byte(checksum>>8))
	return append(header, compressed.Bytes()[10:]...)
}

func TestOptionalHeaderCRC(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags byte
	}{
		{"none", 0},
		{"extra", flagExtra},
		{"name", flagName},
		{"comment", flagComment},
		{"all", flagExtra | flagName | flagComment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := headerCRCStream(t, tc.flags)
			standard, err := gzip.NewReader(bytes.NewReader(stream))
			if err != nil {
				t.Fatalf("standard gzip rejected fixture: %v", err)
			}
			wantHeader := standard.Header
			want, err := io.ReadAll(standard)
			standard.Close()
			if err != nil {
				t.Fatal(err)
			}
			r, err := NewReaderN(bytes.NewReader(stream), 1024, 2)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			for _, mode := range []string{"new", "reset", "multistream"} {
				t.Run(mode, func(t *testing.T) {
					if mode != "new" {
						input := stream
						if mode == "multistream" {
							input = append(append([]byte(nil), stream...), stream...)
						}
						if err := r.Reset(bytes.NewReader(input)); err != nil {
							t.Fatal(err)
						}
					}
					if r.Name != wantHeader.Name || r.Comment != wantHeader.Comment || !bytes.Equal(r.Extra, wantHeader.Extra) {
						t.Fatalf("optional metadata differs: %+v", r.Header)
					}
					got, err := io.ReadAll(r)
					if mode == "multistream" {
						want = append(append([]byte(nil), want...), want...)
					}
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("ReadAll = %q, %v; want %q, nil", got, err, want)
					}
				})
			}
			// Corrupt an optional byte, or the fixed header when none is present,
			// while leaving the stored checksum unchanged.
			if tc.flags == 0 {
				stream[4] ^= 1
			} else if tc.flags&flagExtra != 0 {
				stream[12] ^= 1
			} else {
				stream[10] ^= 1
			}
			if err := r.Reset(bytes.NewReader(stream)); err != ErrHeader {
				t.Fatalf("corrupt header: got %v, want %v", err, ErrHeader)
			}
		})
	}
}
