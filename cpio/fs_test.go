package cpio

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestNewFS(t *testing.T) {
	t.Run("a regular file is readable through Open", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "hello.txt", mode: sIFREG | 0o644, data: []byte("hello world")},
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

	t.Run("a missing parent directory is synthesized", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "usr/bin/hello", mode: sIFREG | 0o755, data: []byte("bin")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "usr/bin")
		if err != nil {
			t.Fatalf("Stat usr/bin: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("usr/bin IsDir = false, want true")
		}
	})

	t.Run("a stored directory entry is reported as a directory", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "etc", mode: sIFDIR | 0o700},
			{name: "etc/conf", mode: sIFREG | 0o644, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "etc")
		if err != nil {
			t.Fatalf("Stat etc: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("etc IsDir = false, want true")
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("etc perm = %o, want 700", perm)
		}
	})

	t.Run("a ./ prefixed name is normalized to an fs path", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "./usr/share/doc", mode: sIFREG | 0o644, data: []byte("doc")},
		})

		fsys := newFS(t, archive)

		if _, err := fs.Stat(fsys, "usr/share/doc"); err != nil {
			t.Fatalf("Stat usr/share/doc: %v", err)
		}
	})

	t.Run("a symlink target is returned by ReadLink", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "link", mode: sIFLNK | 0o777, data: []byte("target/path")},
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
		archive := writeNewc(t, []entry{
			{name: "file", mode: sIFREG | 0o644, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		if _, err := fs.ReadLink(fsys, "file"); err == nil {
			t.Fatal("ReadLink on regular file: got nil error, want error")
		}
	})

	t.Run("owner and group are exposed through the Header in Sys", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "owned", mode: sIFREG | 0o644, uid: 1000, gid: 2000, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "owned")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		hdr, ok := info.Sys().(Header)
		if !ok {
			t.Fatalf("Sys() type = %T, want cpio.Header", info.Sys())
		}
		if hdr.UID != 1000 || hdr.GID != 2000 {
			t.Fatalf("uid/gid = %d/%d, want 1000/2000", hdr.UID, hdr.GID)
		}
	})

	t.Run("a missing entry reports fs.ErrNotExist", func(t *testing.T) {
		fsys := newFS(t, writeNewc(t, nil))

		_, err := fsys.Open("nope")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("it satisfies the io/fs contract", func(t *testing.T) {
		archive := writeNewc(t, []entry{
			{name: "readme", mode: sIFREG | 0o644, data: []byte("top level")},
			{name: "usr/bin/hello", mode: sIFREG | 0o755, data: []byte("#!/bin/sh\n")},
			{name: "usr/lib/data", mode: sIFREG | 0o644, data: []byte("payload")},
		})

		fsys := newFS(t, archive)

		if err := fstest.TestFS(fsys, "readme", "usr/bin/hello", "usr/lib/data"); err != nil {
			t.Fatalf("TestFS: %v", err)
		}
	})
}

func TestNewFSErrors(t *testing.T) {
	t.Run("an unsupported magic is rejected", func(t *testing.T) {
		// odc (portable ASCII) magic, which this package does not support.
		bad := append([]byte("070707"), bytes.Repeat([]byte("0"), headerSize-6)...)

		_, err := NewFS(bytes.NewReader(bad), int64(len(bad)))
		if err == nil {
			t.Fatal("got nil error, want unsupported-magic error")
		}
	})

	t.Run("an archive with no trailer is rejected", func(t *testing.T) {
		// A single valid entry but no TRAILER!!! record.
		var buf bytes.Buffer
		writeEntry(t, &buf, entry{name: "f", mode: sIFREG | 0o644, data: []byte("x")})

		_, err := NewFS(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err == nil {
			t.Fatal("got nil error, want truncated-archive error")
		}
	})
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
	name string
	mode uint32
	uid  int
	gid  int
	data []byte
}

// writeNewc builds a complete newc archive, terminated with the trailer record.
func writeNewc(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, e := range entries {
		writeEntry(t, &buf, e)
	}
	writeEntry(t, &buf, entry{name: trailerName, mode: 0})
	return buf.Bytes()
}

// writeEntry writes one newc record (header, padded name, padded data).
func writeEntry(t *testing.T, w *bytes.Buffer, e entry) {
	t.Helper()
	nameSize := len(e.name) + 1 // include trailing NUL

	fields := []uint32{
		0,                   // ino
		e.mode,              // mode
		uint32(e.uid),       // uid
		uint32(e.gid),       // gid
		1,                   // nlink
		0,                   // mtime
		uint32(len(e.data)), // filesize
		0,                   // devmajor
		0,                   // devminor
		0,                   // rdevmajor
		0,                   // rdevminor
		uint32(nameSize),    // namesize
		0,                   // check
	}

	w.WriteString(magicNewc)
	for _, f := range fields {
		fmt.Fprintf(w, "%08x", f)
	}
	w.WriteString(e.name)
	w.WriteByte(0)
	pad(w, int(align4(int64(headerSize+nameSize)))-(headerSize+nameSize))

	w.Write(e.data)
	pad(w, int(align4(int64(len(e.data))))-len(e.data))
}

func pad(w *bytes.Buffer, n int) {
	for range n {
		w.WriteByte(0)
	}
}
