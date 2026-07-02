package tar

import (
	"archive/tar"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestNewFS(t *testing.T) {
	t.Run("a regular file is readable through Open", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "hello.txt", data: []byte("hello world")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "hello.txt")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "hello world" {
			t.Fatalf("content = %q, want %q", got, "hello world")
		}
	})

	t.Run("a file after another is read from the correct offset", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "first", data: []byte("the first file's contents")},
			{name: "second", data: []byte("second")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "second")
		if err != nil {
			t.Fatalf("ReadFile second: %v", err)
		}
		if string(got) != "second" {
			t.Fatalf("content = %q, want %q", got, "second")
		}
	})

	t.Run("a missing parent directory is synthesized", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "usr/bin/hello", mode: 0o755, data: []byte("bin")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "usr/bin")
		if err != nil {
			t.Fatalf("Stat usr/bin: %v", err)
		}
		if !info.IsDir() {
			t.Fatal("usr/bin IsDir = false, want true")
		}
	})

	t.Run("a stored directory entry keeps its permissions", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "etc", typeflag: tar.TypeDir, mode: 0o700},
			{name: "etc/conf", data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "etc")
		if err != nil {
			t.Fatalf("Stat etc: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("etc perm = %o, want 700", perm)
		}
	})

	t.Run("a symlink target is returned by ReadLink", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "link", typeflag: tar.TypeSymlink, linkname: "target/path"},
		})

		fsys := newFS(t, archive)

		target, err := fs.ReadLink(fsys, "link")
		if err != nil {
			t.Fatalf("ReadLink: %v", err)
		}
		if target != "target/path" {
			t.Fatalf("target = %q, want %q", target, "target/path")
		}
	})

	t.Run("ReadLink on a non-symlink is an error", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "file", data: []byte("x")},
		})

		fsys := newFS(t, archive)

		if _, err := fs.ReadLink(fsys, "file"); err == nil {
			t.Fatal("ReadLink on regular file: got nil error, want error")
		}
	})

	t.Run("a hard link reads the contents of its target", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "original", data: []byte("shared contents")},
			{name: "hardlink", typeflag: tar.TypeLink, linkname: "original"},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "hardlink")
		if err != nil {
			t.Fatalf("ReadFile hardlink: %v", err)
		}
		if string(got) != "shared contents" {
			t.Fatalf("content = %q, want %q", got, "shared contents")
		}
	})

	t.Run("a ./ prefixed name is normalized to an fs path", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "./usr/share/doc", data: []byte("doc")},
		})

		fsys := newFS(t, archive)

		if _, err := fs.Stat(fsys, "usr/share/doc"); err != nil {
			t.Fatalf("Stat usr/share/doc: %v", err)
		}
	})

	t.Run("owner and group are exposed through the tar.Header in Sys", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "owned", uid: 1000, gid: 2000, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "owned")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		hdr, ok := info.Sys().(*tar.Header)
		if !ok {
			t.Fatalf("Sys() type = %T, want *tar.Header", info.Sys())
		}
		if hdr.Uid != 1000 || hdr.Gid != 2000 {
			t.Fatalf("uid/gid = %d/%d, want 1000/2000", hdr.Uid, hdr.Gid)
		}
	})

	t.Run("a name longer than 100 bytes is read via the PAX extension", func(t *testing.T) {
		long := "usr/share/doc/a-package/" + string(bytes.Repeat([]byte("x"), 120)) + ".txt"
		archive := writeTar(t, []entry{
			{name: long, data: []byte("long name payload")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, cleanName(long))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "long name payload" {
			t.Fatalf("content = %q, want %q", got, "long name payload")
		}
	})

	t.Run("a missing entry reports fs.ErrNotExist", func(t *testing.T) {
		fsys := newFS(t, writeTar(t, nil))

		_, err := fsys.Open("nope")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("it satisfies the io/fs contract", func(t *testing.T) {
		archive := writeTar(t, []entry{
			{name: "readme", data: []byte("top level")},
			{name: "usr/bin/hello", mode: 0o755, data: []byte("#!/bin/sh\n")},
			{name: "usr/lib/data", data: []byte("payload")},
		})

		fsys := newFS(t, archive)

		if err := fstest.TestFS(fsys, "readme", "usr/bin/hello", "usr/lib/data"); err != nil {
			t.Fatalf("TestFS: %v", err)
		}
	})
}

// TestAgainstSystemTar validates the reader against an archive produced by the
// system tar binary. It is skipped where tar is unavailable so the suite stays
// hermetic.
func TestAgainstSystemTar(t *testing.T) {
	tarBin, err := exec.LookPath("tar")
	if err != nil {
		t.Skip("system tar not available")
	}

	root := t.TempDir()
	files := map[string]string{
		"debian-binary":      "2.0\n",
		"etc/app/config.ini": "[main]\nkey = value\n",
		"usr/bin/app":        "#!/bin/sh\necho hi\n",
	}
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	archive := filepath.Join(t.TempDir(), "test.tar")
	cmd := exec.Command(tarBin, "cf", archive, "-C", root, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar cf: %v: %s", err, out)
	}

	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}

	fsys, err := NewFS(f, info.Size())
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	for name, content := range files {
		got, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if string(got) != content {
			t.Fatalf("%s content = %q, want %q", name, got, content)
		}
	}
}

// newFS indexes archive and fails the test on error.
func newFS(t *testing.T, archive []byte) *FS {
	t.Helper()
	fsys, err := NewFS(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	return fsys
}

// entry describes a single record for the test writer.
type entry struct {
	name     string
	mode     int64
	uid      int
	gid      int
	typeflag byte
	linkname string
	data     []byte
}

// writeTar builds a tar archive from entries using archive/tar.
func writeTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     mode,
			Uid:      e.uid,
			Gid:      e.gid,
			Typeflag: typeflag,
			Linkname: e.linkname,
			ModTime:  time.Unix(0, 0),
		}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.data))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %q: %v", e.name, err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatalf("Write %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	return buf.Bytes()
}
