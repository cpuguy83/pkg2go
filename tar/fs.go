package tar

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// FS is a read-only [io/fs.FS] backed by an indexed tar archive. It is safe for
// concurrent use: all reads go through [io.ReaderAt] with explicit offsets.
//
// FS implements [fs.FS], [fs.StatFS], [fs.ReadDirFS], and [fs.ReadLinkFS].
type FS struct {
	ra    io.ReaderAt
	root  *node
	index map[string]*node
}

// node is a single entry in the archive tree.
type node struct {
	base       string // final path element
	hdr        *tar.Header
	mode       fs.FileMode
	dataOffset int64 // absolute offset of the file data within the archive
	dataSize   int64 // size of the file data (resolved for hard links)
	isDir      bool
	children   map[string]*node // populated for directories only
}

// NewFS indexes the tar archive in ra, which must contain exactly size bytes,
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
	f.resolveHardLinks()
	return f, nil
}

// build walks the archive sequentially, populating the node tree. It tracks the
// number of bytes consumed from the underlying reader so that, immediately after
// [tar.Reader.Next], the counter marks the start of the current entry's data.
func (f *FS) build(ra io.ReaderAt, size int64) error {
	cr := &countReader{r: io.NewSectionReader(ra, 0, size)}
	tr := tar.NewReader(cr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeGNUSparse ||
			len(hdr.PAXRecords) > 0 && hasSparseRecords(hdr.PAXRecords) {
			return fmt.Errorf("tar: sparse file %q is not supported", hdr.Name)
		}

		dataOffset := cr.n
		f.insert(cleanName(hdr.Name), hdr, dataOffset)
	}
}

// insert places an entry into the tree, synthesizing any missing parent
// directories along the way.
func (f *FS) insert(name string, hdr *tar.Header, dataOffset int64) {
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
			mode := hdr.FileInfo().Mode()
			child.hdr = hdr
			child.mode = mode
			child.dataOffset = dataOffset
			child.dataSize = hdr.Size
			child.isDir = mode.IsDir()
			if child.isDir && child.children == nil {
				child.children = map[string]*node{}
			}
		} else if child.children == nil {
			// Synthesize an intermediate directory.
			child.isDir = true
			child.mode = fs.ModeDir | 0o755
			child.children = map[string]*node{}
		}
		f.index[joinParts(parts[:i+1])] = child
		cur = child
	}
}

// resolveHardLinks points each hard-link node at the data of its target so that
// reading a hard link yields the linked file's contents.
func (f *FS) resolveHardLinks() {
	for _, n := range f.index {
		if n.hdr == nil || n.hdr.Typeflag != tar.TypeLink {
			continue
		}
		target, ok := f.index[cleanName(n.hdr.Linkname)]
		if !ok || target.isDir {
			continue
		}
		n.dataOffset = target.dataOffset
		n.dataSize = target.dataSize
	}
}

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
	if n.mode&fs.ModeSymlink != 0 {
		return &symlinkFile{n: n, r: strings.NewReader(n.hdr.Linkname)}, nil
	}
	return &regFile{
		n:  n,
		sr: io.NewSectionReader(f.ra, n.dataOffset, n.dataSize),
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

// Lstat implements [fs.ReadLinkFS]. Because [FS] never resolves symlinks, Lstat
// behaves identically to [FS.Stat].
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
	if n.mode&fs.ModeSymlink == 0 {
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
		mode:     fs.ModeDir | 0o755,
		isDir:    true,
		children: map[string]*node{},
	}
}

// countReader counts the number of bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// hasSparseRecords reports whether the PAX records describe a GNU sparse file.
func hasSparseRecords(records map[string]string) bool {
	for k := range records {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return true
		}
	}
	return false
}

// cleanName normalizes a stored tar path into an fs path: no leading "./" or
// "/", "." and ".." resolved, trailing slash dropped, root represented as ".".
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
