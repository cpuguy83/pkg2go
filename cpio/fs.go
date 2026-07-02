package cpio

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// FS is a read-only [io/fs.FS] backed by an indexed cpio archive. It is safe
// for concurrent use: all reads go through [io.ReaderAt] with explicit offsets.
//
// FS implements [fs.FS], [fs.StatFS], [fs.ReadDirFS], and [fs.ReadLinkFS].
type FS struct {
	ra   io.ReaderAt
	root *node
	// index maps a cleaned path to its node, including synthesized directories.
	index map[string]*node
}

// node is a single entry in the archive tree.
type node struct {
	base       string // final path element
	hdr        Header
	dataOffset int64 // absolute offset of the file data within the archive
	isDir      bool
	children   map[string]*node // populated for directories only
}

// NewFS indexes the cpio archive in ra, which must contain exactly size bytes,
// and returns an [FS] serving its contents. The archive is read once up front
// to build the index; file data is read lazily on access.
func NewFS(ra io.ReaderAt, size int64) (*FS, error) {
	f := &FS{
		ra:    ra,
		root:  newDir("."),
		index: map[string]*node{},
	}
	f.index["."] = f.root

	if err := f.build(ra, size); err != nil {
		return nil, err
	}
	return f, nil
}

// build walks the archive sequentially, populating the node tree.
func (f *FS) build(ra io.ReaderAt, size int64) error {
	var pos int64
	hb := make([]byte, headerSize)
	for {
		if pos+headerSize > size {
			return errors.New("cpio: truncated archive: missing trailer")
		}
		if _, err := ra.ReadAt(hb, pos); err != nil {
			return err
		}
		rh, err := parseHeader(hb)
		if err != nil {
			return err
		}

		nameBuf := make([]byte, rh.nameSize)
		if _, err := ra.ReadAt(nameBuf, pos+headerSize); err != nil {
			return err
		}
		name := trimName(nameBuf)
		if name == trailerName {
			return nil
		}

		dataOffset := pos + align4(int64(headerSize)+int64(rh.nameSize))
		hdr := rh.toHeader(name)

		if hdr.Mode&fs.ModeSymlink != 0 && hdr.Size > 0 {
			link := make([]byte, hdr.Size)
			if _, err := ra.ReadAt(link, dataOffset); err != nil {
				return err
			}
			hdr.Linkname = string(link)
		}

		f.insert(cleanName(name), hdr, dataOffset)

		pos = dataOffset + align4(hdr.Size)
	}
}

// insert places an entry into the tree, synthesizing any missing parent
// directories along the way.
func (f *FS) insert(name string, hdr Header, dataOffset int64) {
	if name == "." {
		return
	}

	parts := strings.Split(name, "/")
	cur := f.root
	for i, part := range parts {
		last := i == len(parts)-1
		child, ok := cur.children[part]
		if !ok {
			child = &node{base: part}
			cur.children[part] = child
		}

		if last {
			child.hdr = hdr
			child.dataOffset = dataOffset
			child.isDir = hdr.Mode.IsDir()
			if child.isDir && child.children == nil {
				child.children = map[string]*node{}
			}
		} else if child.children == nil {
			// Synthesize an intermediate directory.
			child.isDir = true
			child.children = map[string]*node{}
			if child.hdr.Name == "" {
				child.hdr = Header{Name: part, Mode: fs.ModeDir | 0o755}
			}
		}
		f.index[joinParts(parts[:i+1])] = child
		cur = child
	}
}

// lookup finds a node by cleaned path.
func (f *FS) lookup(name string) (*node, bool) {
	n, ok := f.index[name]
	return n, ok
}

// Open implements [fs.FS]. Opening a symlink returns a file whose contents are
// the raw link target; use [FS.ReadLink] to read the target as a string.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.lookup(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if n.isDir {
		return &dirFile{n: n, entries: n.sortedChildren()}, nil
	}
	return &regFile{
		n:  n,
		sr: io.NewSectionReader(f.ra, n.dataOffset, n.hdr.Size),
	}, nil
}

// Stat implements [fs.StatFS].
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.lookup(name)
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fileInfo{n}, nil
}

// Lstat implements [fs.ReadLinkFS]. Because [FS] never resolves symlinks,
// Lstat behaves identically to [FS.Stat].
func (f *FS) Lstat(name string) (fs.FileInfo, error) {
	return f.Stat(name)
}

// ReadLink implements [fs.ReadLinkFS].
func (f *FS) ReadLink(name string) (string, error) {
	if !fs.ValidPath(name) {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.lookup(name)
	if !ok {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrNotExist}
	}
	if n.hdr.Mode&fs.ModeSymlink == 0 {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
	}
	return n.hdr.Linkname, nil
}

// ReadDir implements [fs.ReadDirFS].
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.lookup(name)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	if !n.isDir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	children := n.sortedChildren()
	entries := make([]fs.DirEntry, len(children))
	for i, c := range children {
		entries[i] = fileInfo{c}
	}
	return entries, nil
}

func (n *node) sortedChildren() []*node {
	out := make([]*node, 0, len(n.children))
	for _, c := range n.children {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].base < out[j].base })
	return out
}

func newDir(base string) *node {
	return &node{
		base:     base,
		hdr:      Header{Name: base, Mode: fs.ModeDir | 0o755},
		isDir:    true,
		children: map[string]*node{},
	}
}

// trimName drops the trailing NUL from a cpio name field.
func trimName(b []byte) string {
	return string(b[:len(b)-1])
}

// cleanName normalizes a stored cpio path into an fs path: no leading "./" or
// "/", "." and ".." resolved, root represented as ".".
func cleanName(name string) string {
	n := path.Clean("/" + name)
	n = strings.TrimPrefix(n, "/")
	if n == "" {
		return "."
	}
	return n
}

func joinParts(parts []string) string {
	return strings.Join(parts, "/")
}
