package rpm

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cpuguy83/pkg2go/compress"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

func TestOpen(t *testing.T) {
	t.Run("a stream without the RPM lead magic is rejected", func(t *testing.T) {
		_, err := Open(bytes.NewReader([]byte("definitely not an rpm package")), 29)
		if err == nil {
			t.Fatal("got nil error, want failure")
		}
	})

	t.Run("a file shorter than the lead is rejected", func(t *testing.T) {
		_, err := Open(bytes.NewReader([]byte{0xed, 0xab, 0xee, 0xdb}), 4)
		if err == nil {
			t.Fatal("got nil error, want failure")
		}
	})
}

func TestPackageRootfs(t *testing.T) {
	codecs := []struct {
		name       string
		codec      compress.Codec
		compressor string // RPMTAG_PAYLOADCOMPRESSOR value
	}{
		{"a gzip payload", compress.Gzip, "gzip"},
		{"an xz payload", compress.Xz, "xz"},
		{"a zstd payload", compress.Zstd, "zstd"},
		{"an lzma payload named only in the header", compress.Lzma, "lzma"},
	}
	for _, tc := range codecs {
		t.Run(tc.name+" is decompressed and its files are served", func(t *testing.T) {
			archive := buildRPM(t, tc.compressor, tc.codec, map[string][]byte{
				"./usr/bin/app":        []byte("the binary"),
				"./etc/app/config.ini": []byte("verbose = true"),
			})

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

	t.Run("a payload with no compressor tag falls back to sniffing the magic", func(t *testing.T) {
		archive := buildRPM(t, "", compress.Gzip, map[string][]byte{
			"./usr/bin/app": []byte("the binary"),
		})

		p := mustOpen(t, archive)
		defer p.Close()

		rootfs, err := p.Rootfs()
		if err != nil {
			t.Fatalf("Rootfs: %v", err)
		}
		assertFileContent(t, rootfs, "usr/bin/app", "the binary")
	})

	t.Run("an uncompressed payload is read directly", func(t *testing.T) {
		archive := buildRPM(t, "", compress.None, map[string][]byte{
			"./usr/bin/app": []byte("the binary"),
		})

		p := mustOpen(t, archive)
		defer p.Close()

		rootfs, err := p.Rootfs()
		if err != nil {
			t.Fatalf("Rootfs: %v", err)
		}
		assertFileContent(t, rootfs, "usr/bin/app", "the binary")
	})
}

func TestPackageFromRpmbuild(t *testing.T) {
	t.Run("a .rpm produced by rpmbuild is read end to end", func(t *testing.T) {
		rpmbuild, err := exec.LookPath("rpmbuild")
		if err != nil {
			t.Skip("rpmbuild not available")
		}

		topdir := t.TempDir()
		specPath := filepath.Join(topdir, "hello.spec")
		writeFile(t, specPath, helloSpec)

		cmd := exec.Command(rpmbuild,
			"--define", "_topdir "+topdir,
			"-bb", specPath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rpmbuild: %v\n%s", err, out)
		}

		matches, err := filepath.Glob(filepath.Join(topdir, "RPMS", "*", "hello-*.rpm"))
		if err != nil || len(matches) == 0 {
			t.Fatalf("locate built rpm: %v (matches=%v)", err, matches)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read built rpm: %v", err)
		}

		p := mustOpen(t, data)
		defer p.Close()

		rootfs, err := p.Rootfs()
		if err != nil {
			t.Fatalf("Rootfs: %v", err)
		}
		assertFileContent(t, rootfs, "usr/bin/hello", "#!/bin/sh\necho hello\n")
	})
}

// --- helpers ---

func mustOpen(t *testing.T, archive []byte) *Package {
	t.Helper()
	p, err := Open(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return p
}

func assertFileContent(t *testing.T, fsys fs.FS, name, want string) {
	t.Helper()
	got, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("ReadFile %q: %v", name, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
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

// buildRPM assembles a real .rpm: a 96-byte lead, an empty signature header, a
// main header carrying the payload format and (optionally) compressor tags, and
// a cpio payload compressed with the requested codec. When compressor is empty
// the RPMTAG_PAYLOADCOMPRESSOR tag is omitted, as rpm does for uncompressed
// payloads.
func buildRPM(t *testing.T, compressor string, codec compress.Codec, files map[string][]byte) []byte {
	t.Helper()

	payload, _ := compressBytes(t, writeCPIO(t, files), codec)

	tags := []stringTagEntry{{tag: 1124, value: "cpio"}} // RPMTAG_PAYLOADFORMAT
	if compressor != "" {
		tags = append(tags, stringTagEntry{tag: tagPayloadCompressor, value: compressor})
	}

	var b bytes.Buffer
	b.Write(buildLead())
	sig := buildHeader(nil)
	b.Write(sig)
	b.Write(make([]byte, (8-(len(sig)%8))%8))
	b.Write(buildHeader(tags))
	b.Write(payload)
	return b.Bytes()
}

func buildLead() []byte {
	lead := make([]byte, leadSize)
	copy(lead, leadMagic)
	lead[4] = 3  // major version
	lead[78] = 5 // signature_type = RPMSIGTYPE_HEADERSIG (ignored by the reader)
	return lead
}

type stringTagEntry struct {
	tag   uint32
	value string
}

func buildHeader(entries []stringTagEntry) []byte {
	var index, store bytes.Buffer
	for _, e := range entries {
		offset := store.Len()
		store.WriteString(e.value)
		store.WriteByte(0)

		writeU32(&index, e.tag)
		writeU32(&index, typeString)
		writeU32(&index, uint32(offset))
		writeU32(&index, 1) // count
	}

	var b bytes.Buffer
	b.Write(headerMagic)
	b.Write([]byte{0, 0, 0, 0}) // reserved
	writeU32(&b, uint32(len(entries)))
	writeU32(&b, uint32(store.Len()))
	b.Write(index.Bytes())
	b.Write(store.Bytes())
	return b.Bytes()
}

func writeU32(b *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	b.Write(buf[:])
}

// writeCPIO produces a newc (SVR4) cpio archive of the given files.
func writeCPIO(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	for name, data := range files {
		writeCPIOEntry(&b, name, 0o100000|0o644, data)
	}
	writeCPIOEntry(&b, "TRAILER!!!", 0, nil)
	return b.Bytes()
}

func writeCPIOEntry(b *bytes.Buffer, name string, mode uint32, data []byte) {
	const headerSize = 110
	nameSize := len(name) + 1
	fields := []uint32{
		0, mode, 0, 0, 1, 0, uint32(len(data)),
		0, 0, 0, 0, uint32(nameSize), 0,
	}
	b.WriteString("070701")
	for _, f := range fields {
		fmt.Fprintf(b, "%08x", f)
	}
	b.WriteString(name)
	b.WriteByte(0)
	pad4(b, headerSize+nameSize)
	b.Write(data)
	pad4(b, len(data))
}

func pad4(b *bytes.Buffer, written int) {
	for written%4 != 0 {
		b.WriteByte(0)
		written++
	}
}

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
	case compress.Lzma:
		var b bytes.Buffer
		w, err := lzma.NewWriter(&b)
		if err != nil {
			t.Fatalf("lzma.NewWriter: %v", err)
		}
		writeAll(t, w, raw)
		closeWriter(t, w)
		return b.Bytes(), ".lzma"
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

const helloSpec = `Name: hello
Version: 1.0
Release: 1
Summary: greeter
License: MIT
BuildArch: noarch

%description
A tiny greeter used to test pkg2go.

%install
mkdir -p %{buildroot}/usr/bin
printf '#!/bin/sh\necho hello\n' > %{buildroot}/usr/bin/hello
chmod 0755 %{buildroot}/usr/bin/hello

%files
/usr/bin/hello
`
