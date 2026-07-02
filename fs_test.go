package pkg2go

import (
	"bytes"
	"fmt"
	"io/fs"
	"strconv"
	"testing"

	stdtar "archive/tar"

	"github.com/cpuguy83/pkg2go/ar"
	"github.com/cpuguy83/pkg2go/cpio"
	"github.com/cpuguy83/pkg2go/tar"
)

func TestNewFS(t *testing.T) {
	t.Run("an ar archive is opened as an ar filesystem", func(t *testing.T) {
		archive := arArchive("control.tar", []byte("control payload"))

		fsys, err := NewFS(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}
		if _, ok := fsys.(*ar.FS); !ok {
			t.Fatalf("fsys type = %T, want *ar.FS", fsys)
		}

		got, err := fs.ReadFile(fsys, "control.tar")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "control payload" {
			t.Fatalf("content = %q, want %q", got, "control payload")
		}
	})

	t.Run("a newc cpio archive is opened as a cpio filesystem", func(t *testing.T) {
		archive := cpioArchive("hello.txt", []byte("hi"))

		fsys, err := NewFS(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}
		if _, ok := fsys.(*cpio.FS); !ok {
			t.Fatalf("fsys type = %T, want *cpio.FS", fsys)
		}

		got, err := fs.ReadFile(fsys, "hello.txt")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "hi" {
			t.Fatalf("content = %q, want %q", got, "hi")
		}
	})

	t.Run("a tar archive is opened as a tar filesystem", func(t *testing.T) {
		archive := tarArchive("data.txt", []byte("tar payload"))

		fsys, err := NewFS(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}
		if _, ok := fsys.(*tar.FS); !ok {
			t.Fatalf("fsys type = %T, want *tar.FS", fsys)
		}

		got, err := fs.ReadFile(fsys, "data.txt")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "tar payload" {
			t.Fatalf("content = %q, want %q", got, "tar payload")
		}
	})

	t.Run("an unrecognized stream is rejected", func(t *testing.T) {
		junk := []byte("this is not any archive we know")

		if _, err := NewFS(bytes.NewReader(junk), int64(len(junk))); err == nil {
			t.Fatal("got nil error, want unrecognized-format error")
		}
	})

	t.Run("a stream shorter than the magic is rejected", func(t *testing.T) {
		short := []byte("!<")

		if _, err := NewFS(bytes.NewReader(short), int64(len(short))); err == nil {
			t.Fatal("got nil error, want unrecognized-format error")
		}
	})
}

// arArchive builds a minimal single-member ar archive.
func arArchive(name string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(arMagic)

	hdr := make([]byte, 60)
	for i := range hdr {
		hdr[i] = ' '
	}
	put := func(off int, s string) { copy(hdr[off:], s) }
	put(0, name+"/") // name, trailing-slash terminated
	put(16, "0")     // mtime
	put(28, "0")     // uid
	put(34, "0")     // gid
	put(40, "644")   // mode (octal)
	put(48, strconv.Itoa(len(data)))
	put(58, "`\n")
	b.Write(hdr)

	b.Write(data)
	if len(data)%2 == 1 {
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// cpioArchive builds a minimal newc cpio archive with a single regular file,
// terminated by the trailer record.
func cpioArchive(name string, data []byte) []byte {
	var b bytes.Buffer
	writeRec := func(name string, data []byte, mode uint32) {
		nameSize := len(name) + 1
		fields := []uint32{
			0, mode, 0, 0, 1, 0, uint32(len(data)),
			0, 0, 0, 0, uint32(nameSize), 0,
		}
		b.WriteString(cpioMagicNewc)
		for _, f := range fields {
			fmt.Fprintf(&b, "%08x", f)
		}
		b.WriteString(name)
		b.WriteByte(0)
		pad4(&b, 110+nameSize)
		b.Write(data)
		pad4(&b, len(data))
	}
	writeRec(name, data, 0o100644)
	writeRec("TRAILER!!!", nil, 0)
	return b.Bytes()
}

// pad4 writes zero bytes to advance n to the next four-byte boundary.
func pad4(b *bytes.Buffer, n int) {
	for i := 0; i < (4-n%4)%4; i++ {
		b.WriteByte(0)
	}
}

// tarArchive builds a minimal single-file tar archive.
func tarArchive(name string, data []byte) []byte {
	var b bytes.Buffer
	tw := stdtar.NewWriter(&b)
	_ = tw.WriteHeader(&stdtar.Header{
		Name:     name,
		Mode:     0o644,
		Typeflag: stdtar.TypeReg,
		Size:     int64(len(data)),
	})
	_, _ = tw.Write(data)
	_ = tw.Close()
	return b.Bytes()
}
