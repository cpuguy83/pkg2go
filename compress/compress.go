// Package compress decompresses a package member into a random-access
// [io.ReaderAt].
//
// The container readers in this module need random access over an
// [io.ReaderAt], but the compressed inner members of a package are sequential
// streams. [Decompress] expands such a member to a temporary file once and
// serves it back as a [*File], which is an [io.ReaderAt] whose [File.Close]
// removes the backing file. This is uniform across every codec, avoiding
// per-codec seekability handling. An uncompressed member is passed through
// directly — no copy and no temporary file.
//
// The gzip and bzip2 codecs come from the standard library; xz and lzma are
// provided by github.com/ulikunitz/xz and zstd by
// github.com/klauspost/compress/zstd.
package compress

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// Codec identifies a compression format.
type Codec string

const (
	None  Codec = ""      // no (recognized) compression
	Gzip  Codec = "gzip"  //
	Bzip2 Codec = "bzip2" //
	Xz    Codec = "xz"    //
	Zstd  Codec = "zstd"  //
	Lzma  Codec = "lzma"  //
)

// magic maps a codec to its leading signature. lzma has no reliable magic and is
// therefore only selectable explicitly via [DecompressAs].
var magics = []struct {
	codec Codec
	magic []byte
}{
	{Gzip, []byte{0x1f, 0x8b}},
	{Bzip2, []byte("BZh")},
	{Xz, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
	{Zstd, []byte{0x28, 0xb5, 0x2f, 0xfd}},
}

// maxMagic is the length of the longest signature we need to peek.
const maxMagic = 6

// File is the decompressed content of a package member exposed as a random-access
// [io.ReaderAt]. Compressed members are backed by a temporary file that
// [File.Close] removes; an uncompressed member is served directly from the
// source, in which case [File.Close] is a no-op.
type File struct {
	ra   io.ReaderAt
	size int64
	tmp  *os.File // non-nil only when backed by a temporary file
}

var (
	_ io.ReaderAt = (*File)(nil)
	_ io.Closer   = (*File)(nil)
)

// ReadAt implements [io.ReaderAt].
func (fl *File) ReadAt(p []byte, off int64) (int, error) { return fl.ra.ReadAt(p, off) }

// Size returns the decompressed length in bytes.
func (fl *File) Size() int64 { return fl.size }

// Close removes the backing temporary file, if any. It is a no-op for an
// uncompressed member served directly from its source.
func (fl *File) Close() error {
	if fl.tmp == nil {
		return nil
	}
	name := fl.tmp.Name()
	err := fl.tmp.Close()
	if rmErr := os.Remove(name); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Detect reports the compression codec of ra from its leading magic. A member
// with no recognized signature reports [None].
func Detect(ra io.ReaderAt) (Codec, error) {
	buf := make([]byte, maxMagic)
	n, err := ra.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return None, err
	}
	buf = buf[:n]
	for _, m := range magics {
		if bytes.HasPrefix(buf, m.magic) {
			return m.codec, nil
		}
	}
	return None, nil
}

// Decompress detects the codec of the size-byte member ra from its leading magic
// and expands it to a temporary file, returning the result as a random-access
// [*File]. An uncompressed member is passed through directly, without copying.
// The caller must [File.Close] the result to release any backing file.
func Decompress(ra io.ReaderAt, size int64) (*File, error) {
	codec, err := Detect(ra)
	if err != nil {
		return nil, err
	}
	return DecompressAs(ra, size, codec)
}

// DecompressAs expands the size-byte member ra using the named codec, returning
// the result as a random-access [*File]. [None] passes the member through
// directly, without copying. The caller must [File.Close] the result to release
// any backing file.
func DecompressAs(ra io.ReaderAt, size int64, codec Codec) (*File, error) {
	if codec == None {
		return &File{ra: io.NewSectionReader(ra, 0, size), size: size}, nil
	}
	dr, err := decoder(io.NewSectionReader(ra, 0, size), codec)
	if err != nil {
		return nil, err
	}
	return spool(dr)
}

// decoder wraps r in the decompressor for codec.
func decoder(r io.Reader, codec Codec) (io.Reader, error) {
	switch codec {
	case Gzip:
		return gzip.NewReader(r)
	case Bzip2:
		return bzip2.NewReader(r), nil
	case Xz:
		return xz.NewReader(r)
	case Zstd:
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return zstdReader{zr}, nil
	case Lzma:
		return lzma.NewReader(r)
	default:
		return nil, fmt.Errorf("compress: unknown codec %q", codec)
	}
}

// spool copies dr into a fresh temporary file and returns it as a [*File].
func spool(dr io.Reader) (*File, error) {
	tmp, err := os.CreateTemp("", "pkg2go-*")
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(tmp, dr)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return &File{ra: tmp, size: n, tmp: tmp}, nil
}

// zstdReader adapts a *zstd.Decoder to io.Reader; the decoder needs no explicit
// close because spool reads it to completion.
type zstdReader struct{ *zstd.Decoder }

func (z zstdReader) Read(p []byte) (int, error) { return z.Decoder.Read(p) }
