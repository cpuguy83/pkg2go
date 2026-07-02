package tar

import (
	"io"
	"io/fs"
	"strings"
	"time"
)

// fileInfo adapts a node to both [fs.FileInfo] and [fs.DirEntry].
type fileInfo struct {
	n *node
}

var (
	_ fs.FileInfo = fileInfo{}
	_ fs.DirEntry = fileInfo{}
)

func (fi fileInfo) Name() string { return fi.n.base }

func (fi fileInfo) Size() int64 {
	if fi.n.isDir {
		return 0
	}
	return fi.n.dataSize
}

func (fi fileInfo) Mode() fs.FileMode { return fi.n.mode }

func (fi fileInfo) ModTime() time.Time {
	if fi.n.hdr == nil {
		return time.Time{}
	}
	return fi.n.hdr.ModTime
}

func (fi fileInfo) IsDir() bool { return fi.n.isDir }

// Sys returns the underlying [archive/tar.Header], or nil for a synthesized
// directory.
func (fi fileInfo) Sys() any {
	if fi.n.hdr == nil {
		return nil
	}
	return fi.n.hdr
}

func (fi fileInfo) Type() fs.FileMode { return fi.n.mode.Type() }

func (fi fileInfo) Info() (fs.FileInfo, error) { return fi, nil }

// regFile is an open regular file.
type regFile struct {
	n  *node
	sr *io.SectionReader
}

var _ fs.File = (*regFile)(nil)

func (f *regFile) Stat() (fs.FileInfo, error) { return fileInfo{f.n}, nil }

func (f *regFile) Read(p []byte) (int, error) { return f.sr.Read(p) }

func (f *regFile) Seek(offset int64, whence int) (int64, error) {
	return f.sr.Seek(offset, whence)
}

func (f *regFile) ReadAt(p []byte, off int64) (int, error) {
	return f.sr.ReadAt(p, off)
}

func (f *regFile) Close() error { return nil }

// symlinkFile is an open symlink, read as its raw target bytes.
type symlinkFile struct {
	n *node
	r *strings.Reader
}

var _ fs.File = (*symlinkFile)(nil)

func (f *symlinkFile) Stat() (fs.FileInfo, error) { return fileInfo{f.n}, nil }

func (f *symlinkFile) Read(p []byte) (int, error) { return f.r.Read(p) }

func (f *symlinkFile) Close() error { return nil }

// dirFile is an open directory supporting [fs.ReadDirFile].
type dirFile struct {
	n       *node
	entries []*node
	pos     int
}

var (
	_ fs.File        = (*dirFile)(nil)
	_ fs.ReadDirFile = (*dirFile)(nil)
)

func (d *dirFile) Stat() (fs.FileInfo, error) { return fileInfo{d.n}, nil }

func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.n.base, Err: fs.ErrInvalid}
}

func (d *dirFile) Close() error { return nil }

// ReadDir implements [fs.ReadDirFile] with the standard partial-read semantics:
// n <= 0 returns all remaining entries; n > 0 returns at most n and reports
// [io.EOF] once exhausted.
func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := len(d.entries) - d.pos
	if n <= 0 {
		out := make([]fs.DirEntry, 0, remaining)
		for _, c := range d.entries[d.pos:] {
			out = append(out, fileInfo{c})
		}
		d.pos = len(d.entries)
		return out, nil
	}

	if remaining == 0 {
		return nil, io.EOF
	}
	if n > remaining {
		n = remaining
	}
	out := make([]fs.DirEntry, 0, n)
	for _, c := range d.entries[d.pos : d.pos+n] {
		out = append(out, fileInfo{c})
	}
	d.pos += n
	return out, nil
}
