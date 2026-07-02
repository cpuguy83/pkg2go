package ar

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

// FS is a read-only [io/fs.FS] backed by an indexed ar archive. It is safe for
// concurrent use: all reads go through [io.ReaderAt] with explicit offsets.
//
// FS implements [fs.FS], [fs.StatFS], and [fs.ReadDirFS]. ar is a flat archive,
// so every member is a regular file directly under the root; there are no
// subdirectories and no symlinks.
type FS struct {
	ra   io.ReaderAt
	root *node
	// index maps a member name (and ".") to its node.
	index map[string]*node
}

// node is a single member, or the synthetic root directory.
type node struct {
	base       string // member name
	hdr        Header
	dataOffset int64 // absolute offset of the member data within the archive
	isDir      bool
	children   map[string]*node // populated for the root only
}

// NewFS indexes the ar archive in ra, which must contain exactly size bytes, and
// returns an [FS] serving its contents. The archive is read once up front to
// build the index; member data is read lazily on access.
func NewFS(ra io.ReaderAt, size int64) (*FS, error) {
	f := &FS{
		ra: ra,
		root: &node{
			base:     ".",
			hdr:      Header{Name: ".", Mode: fs.ModeDir | 0o755},
			isDir:    true,
			children: map[string]*node{},
		},
		index: map[string]*node{},
	}
	f.index["."] = f.root

	if err := f.build(ra, size); err != nil {
		return nil, err
	}
	return f, nil
}

// build walks the archive sequentially, populating the member index.
func (f *FS) build(ra io.ReaderAt, size int64) error {
	if size < int64(len(magic)) {
		return errors.New("ar: not an archive: too short for global header")
	}
	hdr := make([]byte, len(magic))
	if _, err := ra.ReadAt(hdr, 0); err != nil {
		return err
	}
	if string(hdr) != magic {
		return fmt.Errorf("ar: bad global magic %q", string(hdr))
	}

	var nameTable []byte
	pos := int64(len(magic))
	hb := make([]byte, headerSize)
	for pos+headerSize <= size {
		if _, err := ra.ReadAt(hb, pos); err != nil {
			return err
		}
		rh, err := parseHeader(hb)
		if err != nil {
			return err
		}

		dataStart := pos + headerSize
		if dataStart+rh.size > size {
			return fmt.Errorf("ar: member %q extends past end of archive", rh.name)
		}
		next := dataStart + align2(rh.size)

		// The GNU extended-name table is consumed for indexing but never listed.
		if rh.name == gnuNameTable {
			nameTable = make([]byte, rh.size)
			if _, err := ra.ReadAt(nameTable, dataStart); err != nil {
				return err
			}
			pos = next
			continue
		}

		name, dataOffset, dataSize, err := f.resolveMember(rh, nameTable, dataStart)
		if err != nil {
			return err
		}

		if keepMember(name) {
			hdr := rh.toHeader(name)
			hdr.Size = dataSize
			f.insert(name, hdr, dataOffset)
		}

		pos = next
	}
	return nil
}

// resolveMember determines a member's real name and the offset and length of its
// content, accounting for the GNU "/N" and BSD "#1/N" extended-name variants.
func (f *FS) resolveMember(rh rawHeader, nameTable []byte, dataStart int64) (name string, dataOffset, dataSize int64, err error) {
	switch {
	case rh.nameLen > 0: // BSD "#1/N": real name is the data prefix.
		if rh.nameLen > rh.size {
			return "", 0, 0, fmt.Errorf("ar: BSD name length %d exceeds member size %d", rh.nameLen, rh.size)
		}
		nb := make([]byte, rh.nameLen)
		if _, err := f.ra.ReadAt(nb, dataStart); err != nil {
			return "", 0, 0, err
		}
		return trimField(nb), dataStart + rh.nameLen, rh.size - rh.nameLen, nil
	default:
		if off, ok := gnuNameRef(rh.name); ok {
			n, err := resolveTableName(nameTable, off)
			if err != nil {
				return "", 0, 0, err
			}
			return n, dataStart, rh.size, nil
		}
		return cleanShortName(rh.name), dataStart, rh.size, nil
	}
}

// keepMember reports whether a resolved member should be exposed through the FS.
// Symbol tables and anything that is not a valid single-component path are
// hidden.
func keepMember(name string) bool {
	if name == "" || isSymbolTable(name) {
		return false
	}
	return fs.ValidPath(name) && !strings.Contains(name, "/")
}

// insert adds a member directly under the root.
func (f *FS) insert(name string, hdr Header, dataOffset int64) {
	n := &node{base: name, hdr: hdr, dataOffset: dataOffset}
	f.root.children[name] = n
	f.index[name] = n
}

// Open implements [fs.FS].
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.index[name]
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
	n, ok := f.index[name]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fileInfo{n}, nil
}

// ReadDir implements [fs.ReadDirFS]. Only the root directory (".") exists.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	n, ok := f.index[name]
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
