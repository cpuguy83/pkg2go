package pkg2go

import (
	"fmt"
	"io"
	"io/fs"

	"github.com/cpuguy83/pkg2go/ar"
	"github.com/cpuguy83/pkg2go/cpio"
	"github.com/cpuguy83/tar2go"
)

// Archive magics used to detect the format of a stream.
const (
	arMagic          = "!<arch>\n" // ar global header
	cpioMagicNewc    = "070701"    // cpio new ASCII (newc)
	cpioMagicNewcCRC = "070702"    // cpio new ASCII with CRC
	tarMagic         = "ustar"     // tar (POSIX/GNU), at offset 257
	tarMagicOffset   = 257
)

// sniffLen is the number of leading bytes needed to identify the ar and cpio
// formats. It is the length of the longest magic that lives at the start.
const sniffLen = len(arMagic)

// NewFS identifies the archive format of ra, which must contain exactly size
// bytes, and returns an [io/fs.FS] serving its contents. It recognizes ar, newc
// cpio, and tar archives by their magic and delegates to [ar.NewFS],
// [cpio.NewFS], or github.com/cpuguy83/tar2go respectively.
//
// It returns an error if the format is not recognized. Compressed streams are
// not detected; decompress first and pass the resulting [io.ReaderAt].
func NewFS(ra io.ReaderAt, size int64) (fs.FS, error) {
	buf := make([]byte, sniffLen)
	n, err := ra.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	switch {
	case hasPrefix(buf, arMagic):
		return ar.NewFS(ra, size)
	case hasPrefix(buf, cpioMagicNewc), hasPrefix(buf, cpioMagicNewcCRC):
		return cpio.NewFS(ra, size)
	case isTar(ra, size):
		return tar2go.NewIndex(io.NewSectionReader(ra, 0, size)).FS(), nil
	default:
		return nil, fmt.Errorf("pkg2go: unrecognized archive format")
	}
}

// isTar reports whether ra carries the tar "ustar" magic at offset 257.
func isTar(ra io.ReaderAt, size int64) bool {
	if size < int64(tarMagicOffset+len(tarMagic)) {
		return false
	}
	buf := make([]byte, len(tarMagic))
	if _, err := ra.ReadAt(buf, tarMagicOffset); err != nil {
		return false
	}
	return string(buf) == tarMagic
}

func hasPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix
}
