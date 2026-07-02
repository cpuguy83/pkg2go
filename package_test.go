package pkg2go

import (
	"bytes"
	"io/fs"
	"strconv"
	"testing"

	"github.com/cpuguy83/pkg2go/deb"
	"github.com/cpuguy83/pkg2go/rpm"
)

func TestOpenPackage(t *testing.T) {
	t.Run("an ar archive is opened as a deb package", func(t *testing.T) {
		archive := debArchive("usr/bin/app", []byte("the binary"))

		pkg, err := OpenPackage(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatalf("OpenPackage: %v", err)
		}
		defer pkg.Close()
		if _, ok := pkg.(*deb.Package); !ok {
			t.Fatalf("pkg type = %T, want *deb.Package", pkg)
		}
		assertRootfsFile(t, pkg, "usr/bin/app", "the binary")
	})

	t.Run("a stream with the rpm lead is opened as an rpm package", func(t *testing.T) {
		archive := rpmArchive("usr/bin/app", []byte("the binary"))

		pkg, err := OpenPackage(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			t.Fatalf("OpenPackage: %v", err)
		}
		defer pkg.Close()
		if _, ok := pkg.(*rpm.Package); !ok {
			t.Fatalf("pkg type = %T, want *rpm.Package", pkg)
		}
		assertRootfsFile(t, pkg, "usr/bin/app", "the binary")
	})

	t.Run("an unrecognized stream is rejected", func(t *testing.T) {
		junk := []byte("neither a deb nor an rpm package")

		if _, err := OpenPackage(bytes.NewReader(junk), int64(len(junk))); err == nil {
			t.Fatal("got nil error, want unrecognized-format error")
		}
	})
}

// --- helpers ---

func assertRootfsFile(t *testing.T, pkg Package, name, want string) {
	t.Helper()
	rootfs, err := pkg.Rootfs()
	if err != nil {
		t.Fatalf("Rootfs: %v", err)
	}
	got, err := fs.ReadFile(rootfs, name)
	if err != nil {
		t.Fatalf("ReadFile %q: %v", name, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

// debArchive builds a minimal .deb: an ar archive of a debian-binary member and
// an uncompressed data.tar carrying the given file.
func debArchive(name string, data []byte) []byte {
	return arMulti([]arMember{
		{"debian-binary", []byte("2.0\n")},
		{"data.tar", tarArchive(name, data)},
	})
}

// rpmArchive builds a minimal .rpm: a lead, empty signature and main headers,
// and an uncompressed cpio payload carrying the given file.
func rpmArchive(name string, data []byte) []byte {
	var b bytes.Buffer

	lead := make([]byte, 96)
	copy(lead, rpmMagic)
	b.Write(lead)

	b.Write(emptyRPMHeader()) // signature header (16 bytes, 8-byte aligned)
	b.Write(emptyRPMHeader()) // main header
	b.Write(cpioArchive(name, data))
	return b.Bytes()
}

// emptyRPMHeader is a header intro with no index entries and an empty store.
func emptyRPMHeader() []byte {
	return []byte{
		0x8e, 0xad, 0xe8, 0x01, // magic + version
		0, 0, 0, 0, // reserved
		0, 0, 0, 0, // nindex = 0
		0, 0, 0, 0, // hsize = 0
	}
}

type arMember struct {
	name string
	data []byte
}

// arMulti builds an ar archive from the given members, using GNU trailing-slash
// short names and even-byte member padding.
func arMulti(members []arMember) []byte {
	var b bytes.Buffer
	b.WriteString(arMagic)
	for _, m := range members {
		hdr := make([]byte, 60)
		for i := range hdr {
			hdr[i] = ' '
		}
		copy(hdr[0:], m.name+"/")
		copy(hdr[16:], "0")   // mtime
		copy(hdr[28:], "0")   // uid
		copy(hdr[34:], "0")   // gid
		copy(hdr[40:], "644") // mode
		copy(hdr[48:], strconv.Itoa(len(m.data)))
		copy(hdr[58:], "`\n")
		b.Write(hdr)

		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}
