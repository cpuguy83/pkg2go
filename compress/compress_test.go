package compress

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

func TestDecompress(t *testing.T) {
	payload := bytes.Repeat([]byte("pkg2go payload — the quick brown fox\n"), 64)

	t.Run("a gzip stream is detected and decompressed", func(t *testing.T) {
		file := decompress(t, gzipBytes(t, payload))
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("a bzip2 stream is detected and decompressed", func(t *testing.T) {
		file := decompress(t, bzip2Bytes(t, payload))
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("an xz stream is detected and decompressed", func(t *testing.T) {
		file := decompress(t, xzBytes(t, payload))
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("a zstd stream is detected and decompressed", func(t *testing.T) {
		file := decompress(t, zstdBytes(t, payload))
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("an uncompressed member passes through unchanged", func(t *testing.T) {
		file := decompress(t, payload)
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("an uncompressed member is served without a temp file", func(t *testing.T) {
		file := decompress(t, payload)
		defer file.Close()

		if file.tmp != nil {
			t.Fatalf("uncompressed member was copied to temp file %q", file.tmp.Name())
		}
	})

	t.Run("the reported size is the decompressed length", func(t *testing.T) {
		file := decompress(t, gzipBytes(t, payload))
		defer file.Close()

		if file.Size() != int64(len(payload)) {
			t.Fatalf("Size = %d, want %d", file.Size(), len(payload))
		}
	})

	t.Run("random access via ReadAt returns bytes at the offset", func(t *testing.T) {
		file := decompress(t, gzipBytes(t, payload))
		defer file.Close()

		buf := make([]byte, 10)
		if _, err := file.ReadAt(buf, 5); err != nil {
			t.Fatalf("ReadAt: %v", err)
		}
		if !bytes.Equal(buf, payload[5:15]) {
			t.Fatalf("ReadAt = %q, want %q", buf, payload[5:15])
		}
	})

	t.Run("Close removes the backing temp file", func(t *testing.T) {
		file := decompress(t, gzipBytes(t, payload))
		name := file.tmp.Name()

		if err := file.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("temp file still exists after Close: %v", err)
		}
	})
}

func TestDecompressAs(t *testing.T) {
	payload := []byte("explicit codec selection for lzma, which has no reliable magic\n")

	t.Run("an lzma member is decompressed when the codec is named", func(t *testing.T) {
		in := lzmaBytes(t, payload)
		file, err := DecompressAs(bytes.NewReader(in), int64(len(in)), Lzma)
		if err != nil {
			t.Fatalf("DecompressAs: %v", err)
		}
		defer file.Close()

		assertContent(t, file, payload)
	})

	t.Run("an unknown codec is rejected", func(t *testing.T) {
		if _, err := DecompressAs(bytes.NewReader(payload), int64(len(payload)), Codec("bogus")); err == nil {
			t.Fatal("got nil error, want unknown-codec error")
		}
	})
}

func decompress(t *testing.T, in []byte) *File {
	t.Helper()
	file, err := Decompress(bytes.NewReader(in), int64(len(in)))
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	return file
}

func assertContent(t *testing.T, ra io.ReaderAt, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	if _, err := ra.ReadAt(got, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	mustWrite(t, w, data)
	mustClose(t, w)
	return buf.Bytes()
}

func bzip2Bytes(t *testing.T, data []byte) []byte {
	t.Helper()
	// The standard library has no bzip2 writer, so shell out to the system
	// bzip2 to produce a real fixture; skip when it is unavailable.
	bzip2Bin, err := exec.LookPath("bzip2")
	if err != nil {
		t.Skip("system bzip2 not available")
	}
	cmd := exec.Command(bzip2Bin, "-c")
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("bzip2 -c: %v", err)
	}
	return out.Bytes()
}

func xzBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	if err != nil {
		t.Fatalf("xz.NewWriter: %v", err)
	}
	mustWrite(t, w, data)
	mustClose(t, w)
	return buf.Bytes()
}

func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	mustWrite(t, w, data)
	mustClose(t, w)
	return buf.Bytes()
}

func lzmaBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := lzma.NewWriter(&buf)
	if err != nil {
		t.Fatalf("lzma.NewWriter: %v", err)
	}
	mustWrite(t, w, data)
	mustClose(t, w)
	return buf.Bytes()
}

func mustWrite(t *testing.T, w io.Writer, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func mustClose(t *testing.T, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
