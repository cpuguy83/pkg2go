package deb

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	stdtar "archive/tar"

	"github.com/cpuguy83/pkg2go/compress"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

func TestOpen(t *testing.T) {
	t.Run("a stream that is not an ar archive is rejected", func(t *testing.T) {
		_, err := Open(bytes.NewReader([]byte("not an archive at all")), 21)
		if err == nil {
			t.Fatal("got nil error, want failure")
		}
	})

	t.Run("an ar archive without a debian-binary member is rejected", func(t *testing.T) {
		archive := arWrite([]arMember{{"data.tar.gz", gzipTar(t, map[string][]byte{"x": []byte("y")})}})

		_, err := Open(bytes.NewReader(archive), int64(len(archive)))
		if err == nil {
			t.Fatal("got nil error, want failure")
		}
	})
}

func TestPackageRootfs(t *testing.T) {
	codecs := []struct {
		name  string
		codec compress.Codec
	}{
		{"an uncompressed data.tar", compress.None},
		{"a gzip data.tar", compress.Gzip},
		{"an xz data.tar", compress.Xz},
		{"a zstd data.tar", compress.Zstd},
	}
	for _, tc := range codecs {
		t.Run(tc.name+" is decompressed and its files are served", func(t *testing.T) {
			archive := buildDeb(t, sampleControl, nil, map[string][]byte{
				"usr/bin/app":        []byte("the binary"),
				"etc/app/config.ini": []byte("verbose = true"),
			}, tc.codec)

			p := mustOpen(t, archive)
			defer p.Close()

			rootfs, err := p.Rootfs()
			if err != nil {
				t.Fatalf("Rootfs: %v", err)
			}
			assertFileContent(t, rootfs, "usr/bin/app", "the binary")
			assertFileContent(t, rootfs, "etc/app/config.ini", "verbose = true")
		})
	}
}

func TestPackageFromDpkgDeb(t *testing.T) {
	t.Run("a .deb produced by dpkg-deb is read end to end", func(t *testing.T) {
		dpkg, err := exec.LookPath("dpkg-deb")
		if err != nil {
			t.Skip("dpkg-deb not available")
		}

		root := t.TempDir()
		writeFile(t, filepath.Join(root, "DEBIAN", "control"), sampleControl)
		writeFile(t, filepath.Join(root, "usr", "bin", "app"), "the binary")

		debPath := filepath.Join(t.TempDir(), "hello.deb")
		cmd := exec.Command(dpkg, "--build", "--root-owner-group", root, debPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("dpkg-deb --build: %v\n%s", err, out)
		}

		archive, err := os.ReadFile(debPath)
		if err != nil {
			t.Fatalf("read built .deb: %v", err)
		}
		p := mustOpen(t, archive)
		defer p.Close()

		rootfs, err := p.Rootfs()
		if err != nil {
			t.Fatalf("Rootfs: %v", err)
		}
		assertFileContent(t, rootfs, "usr/bin/app", "the binary")
	})
}

// --- helpers ---

const sampleControl = `Package: hello
Version: 2.10-3
Architecture: amd64
Maintainer: Alice <alice@example.com>
Description: example package
 a friendly greeter
 with two lines
`

var minimalData = map[string][]byte{"usr/share/doc/hello/README": []byte("hi")}

func mustOpen(t *testing.T, archive []byte) *Package {
	t.Helper()
	p, err := Open(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func assertFileContent(t *testing.T, fsys fs.FS, name, want string) {
	t.Helper()
	got, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("ReadFile %q: %v", name, err)
	}
	if string(got) != want {
		t.Fatalf("%q = %q, want %q", name, got, want)
	}
}

// buildDeb assembles a real .deb: a debian-binary member, a gzip control.tar
// carrying the control file and any extra control files, and a data.tar
// compressed with the requested codec.
func buildDeb(t *testing.T, control string, controlExtra, dataFiles map[string][]byte, dataCodec compress.Codec) []byte {
	t.Helper()

	controlFiles := map[string][]byte{"control": []byte(control)}
	for name, data := range controlExtra {
		controlFiles[name] = data
	}

	controlTar := gzipTar(t, controlFiles)
	dataRaw := tarBytes(t, dataFiles)
	dataBytes, dataExt := compressBytes(t, dataRaw, dataCodec)

	return arWrite([]arMember{
		{debianBinaryMember, []byte("2.0\n")},
		{"control.tar.gz", controlTar},
		{"data.tar" + dataExt, dataBytes},
	})
}

func gzipTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	out, _ := compressBytes(t, tarBytes(t, files), compress.Gzip)
	return out
}

func tarBytes(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := stdtar.NewWriter(&b)
	for name, data := range files {
		if err := tw.WriteHeader(&stdtar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(data)),
			Typeflag: stdtar.TypeReg,
		}); err != nil {
			t.Fatalf("tar WriteHeader: %v", err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar Write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	return b.Bytes()
}

// compressBytes compresses raw with the given codec, returning the bytes and the
// filename extension a real .deb would use for that codec.
func compressBytes(t *testing.T, raw []byte, codec compress.Codec) (data []byte, ext string) {
	t.Helper()
	switch codec {
	case compress.None:
		return raw, ""
	case compress.Gzip:
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		writeAll(t, w, raw)
		closeWriter(t, w)
		return b.Bytes(), ".gz"
	case compress.Xz:
		var b bytes.Buffer
		w, err := xz.NewWriter(&b)
		if err != nil {
			t.Fatalf("xz.NewWriter: %v", err)
		}
		writeAll(t, w, raw)
		closeWriter(t, w)
		return b.Bytes(), ".xz"
	case compress.Zstd:
		var b bytes.Buffer
		w, err := zstd.NewWriter(&b)
		if err != nil {
			t.Fatalf("zstd.NewWriter: %v", err)
		}
		writeAll(t, w, raw)
		closeWriter(t, w)
		return b.Bytes(), ".zst"
	default:
		t.Fatalf("unsupported test codec %q", codec)
		return nil, ""
	}
}

func writeAll(t *testing.T, w io.Writer, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func closeWriter(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// arMember is a single member to write into a test ar archive.
type arMember struct {
	name string
	data []byte
}

// arWrite builds an ar archive from members, using GNU short names (a trailing
// "/" terminator) and even-byte data padding.
func arWrite(members []arMember) []byte {
	var b bytes.Buffer
	b.WriteString("!<arch>\n")
	for _, m := range members {
		writeArHeader(&b, m.name, len(m.data))
		b.Write(m.data)
		if len(m.data)%2 == 1 {
			b.WriteByte('\n')
		}
	}
	return b.Bytes()
}

func writeArHeader(b *bytes.Buffer, name string, size int) {
	field := func(s string, width int) {
		if len(s) > width {
			s = s[:width]
		}
		b.WriteString(s)
		for i := len(s); i < width; i++ {
			b.WriteByte(' ')
		}
	}
	field(name+"/", 16)           // name (GNU trailing-slash terminator)
	field("0", 12)                // mtime
	field("0", 6)                 // uid
	field("0", 6)                 // gid
	field("100644", 8)            // mode (octal)
	field(strconv.Itoa(size), 10) // size (decimal)
	b.WriteString("`\n")          // terminator
}
