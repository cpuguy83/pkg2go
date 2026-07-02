package pkg2go

import (
	"fmt"
	"io"
	"io/fs"

	"github.com/cpuguy83/pkg2go/ar"
	"github.com/cpuguy83/pkg2go/cpio"
)

// Archive magics used to detect the format of a stream.
const (
	arMagic          = "!<arch>\n" // ar global header
	cpioMagicNewc    = "070701"    // cpio new ASCII (newc)
	cpioMagicNewcCRC = "070702"    // cpio new ASCII with CRC
)

// sniffLen is the number of leading bytes needed to identify any supported
// format. It is the length of the longest magic.
const sniffLen = len(arMagic)

// NewFS identifies the archive format of ra, which must contain exactly size
// bytes, and returns an [io/fs.FS] serving its contents. It recognizes ar
// archives and newc cpio archives by their leading magic and delegates to
// [ar.NewFS] or [cpio.NewFS] respectively.
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
	default:
		return nil, fmt.Errorf("pkg2go: unrecognized archive format")
	}
}

func hasPrefix(b []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix
}
