package rpm

import (
	"fmt"
	"io"
	"io/fs"
	"sync"

	"github.com/cpuguy83/pkg2go/compress"
	"github.com/cpuguy83/pkg2go/cpio"
)

// Package is an opened RPM package. Its methods are safe for concurrent use;
// the payload is decompressed at most once and memoized.
type Package struct {
	ra      io.ReaderAt
	archive *archive

	rootfsOnce sync.Once
	rootfs     fs.FS
	rootfsErr  error

	closersMu sync.Mutex
	closers   []io.Closer
}

// Open parses the lead and headers of the RPM in ra, which must contain exactly
// size bytes, and returns a [Package]. The compressed cpio payload is not read
// until [Package.Rootfs] is called. The returned Package must be closed to
// release the temporary file backing the decompressed payload.
func Open(ra io.ReaderAt, size int64) (*Package, error) {
	a, err := parse(ra, size)
	if err != nil {
		return nil, err
	}
	return &Package{ra: ra, archive: a}, nil
}

// Rootfs returns the files the package installs — the contents of its cpio
// payload — as an [io/fs.FS] rooted at the filesystem root. The payload is
// decompressed on first call and reused thereafter.
func (p *Package) Rootfs() (fs.FS, error) {
	p.rootfsOnce.Do(func() {
		p.rootfs, p.rootfsErr = p.payloadFS()
	})
	return p.rootfs, p.rootfsErr
}

// Close releases the temporary file backing the decompressed payload. It does
// not close the [io.ReaderAt] passed to [Open].
func (p *Package) Close() error {
	p.closersMu.Lock()
	defer p.closersMu.Unlock()
	var err error
	for _, c := range p.closers {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	p.closers = nil
	return err
}

// payloadFS decompresses the payload and returns its cpio contents as an
// [io/fs.FS]. The decompressed payload is registered for cleanup by
// [Package.Close].
func (p *Package) payloadFS() (fs.FS, error) {
	payload := io.NewSectionReader(p.ra, p.archive.payloadOffset, p.archive.payloadSize)

	var (
		decompressed *compress.File
		err          error
	)
	if codec, ok := codecFor(p.archive.compressor); ok {
		decompressed, err = compress.DecompressAs(payload, p.archive.payloadSize, codec)
	} else {
		// The compressor is unnamed (uncompressed payload) or unrecognized;
		// fall back to sniffing the magic, which also passes an uncompressed
		// payload straight through.
		decompressed, err = compress.Decompress(payload, p.archive.payloadSize)
	}
	if err != nil {
		return nil, fmt.Errorf("rpm: decompressing payload: %w", err)
	}
	p.closersMu.Lock()
	p.closers = append(p.closers, decompressed)
	p.closersMu.Unlock()

	return cpio.NewFS(decompressed, decompressed.Size())
}

// codecFor maps a RPMTAG_PAYLOADCOMPRESSOR value to a compress codec. lzma in
// particular must be named because it has no reliable magic to sniff.
func codecFor(name string) (compress.Codec, bool) {
	switch name {
	case "gzip":
		return compress.Gzip, true
	case "bzip2":
		return compress.Bzip2, true
	case "xz":
		return compress.Xz, true
	case "zstd":
		return compress.Zstd, true
	case "lzma":
		return compress.Lzma, true
	default:
		return compress.None, false
	}
}
