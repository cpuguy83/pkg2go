package deb

import (
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"

	"github.com/cpuguy83/pkg2go/ar"
	"github.com/cpuguy83/pkg2go/compress"
	"github.com/cpuguy83/tar2go"
)

// Member names within the outer ar archive.
const (
	debianBinaryMember = "debian-binary"
	dataMember         = "data.tar"
)

// Package is an opened Debian .deb archive. Its methods are safe for concurrent
// use; the data member is decompressed at most once and memoized.
type Package struct {
	ar       *ar.FS
	dataName string // ar member name of the data tarball, "" if absent

	rootfsOnce sync.Once
	rootfs     fs.FS
	rootfsErr  error

	closersMu sync.Mutex
	closers   []io.Closer
}

// Open reads the ar directory of the .deb in ra, which must contain exactly size
// bytes, and returns a [Package]. Only the outer ar archive is indexed; the tar
// members are decompressed lazily. The returned Package must be closed to
// release the temporary files backing any compressed members.
func Open(ra io.ReaderAt, size int64) (*Package, error) {
	afs, err := ar.NewFS(ra, size)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(afs, ".")
	if err != nil {
		return nil, err
	}

	p := &Package{ar: afs}
	var hasDebianBinary bool
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == debianBinaryMember:
			hasDebianBinary = true
		case p.dataName == "" && isDataMember(name):
			p.dataName = name
		}
	}
	if !hasDebianBinary {
		return nil, fmt.Errorf("deb: not a Debian archive: missing %q member", debianBinaryMember)
	}
	return p, nil
}

// Rootfs returns the files the package installs — the contents of its data.tar.*
// member — as an [io/fs.FS] rooted at the filesystem root. The member is
// decompressed on first call and reused thereafter.
func (p *Package) Rootfs() (fs.FS, error) {
	p.rootfsOnce.Do(func() {
		p.rootfs, p.rootfsErr = p.tarMember(p.dataName, dataMember)
	})
	return p.rootfs, p.rootfsErr
}

// Close releases the temporary files backing any decompressed members. It does
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

// tarMember opens the named ar member, decompresses it, and returns its tar
// contents as an [io/fs.FS]. The decompressed member is registered for cleanup
// by [Package.Close].
func (p *Package) tarMember(name, kind string) (fs.FS, error) {
	if name == "" {
		return nil, fmt.Errorf("deb: no %s member", kind)
	}
	member, err := p.ar.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := member.Stat()
	if err != nil {
		return nil, err
	}
	ra, ok := member.(io.ReaderAt)
	if !ok {
		return nil, fmt.Errorf("deb: member %q does not support random access", name)
	}

	decompressed, err := compress.Decompress(ra, info.Size())
	if err != nil {
		return nil, fmt.Errorf("deb: decompressing %q: %w", name, err)
	}
	p.closersMu.Lock()
	p.closers = append(p.closers, decompressed)
	p.closersMu.Unlock()

	return tar2go.NewIndex(decompressed).FS(), nil
}

// isDataMember reports whether an ar member name is the data tarball: either the
// uncompressed data.tar or a compressed data.tar.<ext> (e.g. data.tar.zst).
func isDataMember(name string) bool {
	return name == dataMember || strings.HasPrefix(name, dataMember+".")
}
