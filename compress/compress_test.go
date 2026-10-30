package compress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDecoderReadCloser(t *testing.T) {
	payload := bytes.Repeat([]byte("decoder payload\n"), 64)
	for _, tc := range []struct {
		codec  Codec
		encode func(*testing.T, []byte) []byte
	}{
		{Gzip, gzipBytes},
		{Bzip2, bzip2Bytes},
		{Xz, xzBytes},
		{Zstd, zstdBytes},
		{Lzma, lzmaBytes},
	} {
		t.Run(string(tc.codec), func(t *testing.T) {
			in := tc.encode(t, payload)
			t.Setenv("PATH", t.TempDir())
			t.Run("read to EOF", func(t *testing.T) {
				dr, err := decoder(bytes.NewReader(in), tc.codec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { mustClose(t, dr) })
				got, err := io.ReadAll(dr)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("decoded content = %q, want %q", got, payload)
				}
			})
			t.Run("close early", func(t *testing.T) {
				dr, err := decoder(bytes.NewReader(in), tc.codec)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { mustClose(t, dr) })
				if _, err := io.ReadFull(dr, make([]byte, 1)); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestXZBackends(t *testing.T) {
	payload := bytes.Repeat([]byte("xz payload\n"), 64)
	in := xzBytes(t, payload)

	for _, backend := range []struct {
		name  string
		setup func(*testing.T)
	}{
		{
			name: "binary",
			setup: func(t *testing.T) {
				t.Helper()
				bin, err := exec.LookPath("xz")
				if err != nil {
					t.Skip("system xz not available")
				}
				t.Setenv("PATH", filepath.Dir(bin))
			},
		},
		{
			name: "Go fallback",
			setup: func(t *testing.T) {
				t.Helper()
				t.Setenv("PATH", t.TempDir())
			},
		},
	} {
		t.Run(backend.name, func(t *testing.T) {
			backend.setup(t)
			t.Setenv("XZ_DEFAULTS", "--invalid-pkg2go-option")
			t.Setenv("XZ_OPT", "--single-stream")
			tmpDir := t.TempDir()
			t.Setenv("TMPDIR", tmpDir)

			t.Run("decoder returns a streaming reader", func(t *testing.T) {
				dr, err := decoder(bytes.NewReader(in), Xz)
				if err != nil {
					t.Fatal(err)
				}
				defer dr.Close()
				got, err := io.ReadAll(dr)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("decoded content = %q, want %q", got, payload)
				}
				if n, err := dr.Read(make([]byte, 1)); n != 0 || err != io.EOF {
					t.Fatalf("read after EOF = (%d, %v), want (0, EOF)", n, err)
				}
			})

			for _, tc := range []struct {
				name       string
				decompress func(io.ReaderAt, int64) (*File, error)
			}{
				{name: "detected", decompress: Decompress},
				{
					name: "explicit",
					decompress: func(ra io.ReaderAt, size int64) (*File, error) {
						return DecompressAs(ra, size, Xz)
					},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					file, err := tc.decompress(bytes.NewReader(in), int64(len(in)))
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					assertContent(t, file, payload)
					if file.Size() != int64(len(payload)) {
						t.Fatalf("Size = %d, want %d", file.Size(), len(payload))
					}
					buf := make([]byte, 10)
					if _, err := file.ReadAt(buf, 5); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(buf, payload[5:15]) {
						t.Fatalf("ReadAt = %q, want %q", buf, payload[5:15])
					}
				})
			}

			t.Run("concatenated streams", func(t *testing.T) {
				streams := bytes.Repeat(in, 2)
				file := decompress(t, streams)
				defer file.Close()
				assertContent(t, file, bytes.Repeat(payload, 2))
				if file.Size() != int64(2*len(payload)) {
					t.Fatalf("Size = %d, want %d", file.Size(), 2*len(payload))
				}
			})

			t.Run("input is limited to the member size", func(t *testing.T) {
				trailing := append(bytes.Clone(in), []byte("not part of the member")...)
				file, err := DecompressAs(bytes.NewReader(trailing), int64(len(in)), Xz)
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				assertContent(t, file, payload)
			})

			t.Run("truncated stream is rejected", func(t *testing.T) {
				file, err := Decompress(bytes.NewReader(in), int64(len(in)-1))
				if err == nil || file != nil {
					t.Fatalf("got (%v, %v), want nil file and error", file, err)
				}
			})

			entries, err := os.ReadDir(tmpDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary files were not removed: %v", entries)
			}
		})
	}
}

func TestXZBinaryReaderCloseEarly(t *testing.T) {
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\"\nwhile :; do printf 'output\\n'; done\n"
	setupXZBinary(t, script)
	dr, err := decoder(strings.NewReader(""), Xz)
	if err != nil {
		t.Fatal(err)
	}
	defer dr.Close()
	pid, err := bufio.NewReader(dr).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.Close(); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("/bin/sh", "-c", `kill -0 "$1" 2>/dev/null`, "sh", strings.TrimSpace(pid))
	err = probe.Run()
	if err == nil {
		t.Fatal("decoder process still exists after Close")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("check decoder process: %v", err)
	}
	if err := dr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := dr.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after Close = %v, want closed error", err)
	}
}

func TestXZBinaryReaderLazyStart(t *testing.T) {
	script := "#!/bin/sh\nprintf started > \"$PKG2GO_XZ_MARKER\"\nprintf output\n"
	setupXZBinary(t, script)
	setup := func(t *testing.T) (io.ReadCloser, string) {
		t.Helper()
		marker := filepath.Join(t.TempDir(), "started")
		t.Setenv("PKG2GO_XZ_MARKER", marker)
		dr, err := decoder(strings.NewReader(""), Xz)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { mustClose(t, dr) })
		if n, err := dr.Read(nil); n != 0 || err != nil {
			t.Fatalf("empty read = (%d, %v), want (0, nil)", n, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unexpected process marker before reading: %v", err)
		}
		return dr, marker
	}
	t.Run("read", func(t *testing.T) {
		dr, marker := setup(t)
		got, err := io.ReadAll(dr)
		if err != nil || string(got) != "output" {
			t.Fatalf("ReadAll = (%q, %v), want (output, nil)", got, err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("process did not create its marker: %v", err)
		}
	})
	t.Run("close without reading", func(t *testing.T) {
		dr, marker := setup(t)
		mustClose(t, dr)
		if _, err := dr.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("read after Close = %v, want closed error", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unexpected process marker: %v", err)
		}
	})
	t.Run("temp file failure", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "started")
		t.Setenv("PKG2GO_XZ_MARKER", marker)
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		if file, err := DecompressAs(strings.NewReader(""), 0, Xz); err == nil || file != nil {
			t.Fatalf("got (%v, %v), want nil file and error", file, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("unexpected process marker: %v", err)
		}
	})
}

func TestXZBinaryInvocation(t *testing.T) {
	in := xzBytes(t, []byte("Go decoder output"))

	t.Run("binary is preferred", func(t *testing.T) {
		script := "#!/bin/sh\n" +
			`[ "$#" -eq 3 ] && [ "$1" = "--decompress" ] && [ "$2" = "--stdout" ] && [ "$3" = "--format=xz" ] || exit 99` + "\n" +
			"while IFS= read -r line; do :; done\n" +
			"printf 'binary output'\n"
		tmpDir := setupXZBinary(t, script)
		file, err := DecompressAs(bytes.NewReader(in), int64(len(in)), Xz)
		if err != nil {
			t.Fatal(err)
		}
		assertContent(t, file, []byte("binary output"))
		mustClose(t, file)

		dr, err := decoder(bytes.NewReader(in), Xz)
		if err != nil {
			t.Fatal(err)
		}
		defer dr.Close()
		got, err := io.ReadAll(dr)
		if err != nil || string(got) != "binary output" {
			t.Fatalf("ReadAll = (%q, %v), want (binary output, nil)", got, err)
		}
		if n, err := dr.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			t.Fatalf("read after completion = (%d, %v), want (0, EOF)", n, err)
		}
		assertNoTempFiles(t, tmpDir)
	})

	for _, tc := range []struct {
		name        string
		script      string
		wantOutput  string
		assertError func(*testing.T, error)
	}{
		{
			name:       "exit error is not retried",
			script:     "#!/bin/sh\nprintf partial\nprintf 'decoder failed' >&2\nexit 42\n",
			wantOutput: "partial",
			assertError: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "decoder failed") {
					t.Fatalf("got %v, want error containing decoder failed", err)
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
					t.Fatalf("got %v, want wrapped exit status 42", err)
				}
			},
		},
		{
			name:   "start error is not retried",
			script: "#!/nonexistent-pkg2go-interpreter\n",
			assertError: func(t *testing.T, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "compress: xz:") {
					t.Fatalf("got %v, want error containing compress: xz:", err)
				}
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("got %v, want not-exist error", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := setupXZBinary(t, tc.script)

			file, err := DecompressAs(bytes.NewReader(in), int64(len(in)), Xz)
			if file != nil {
				t.Fatalf("got file %v, want nil", file)
			}
			tc.assertError(t, err)
			t.Run("decoder propagates output and errors", func(t *testing.T) {
				dr, err := decoder(bytes.NewReader(in), Xz)
				if err != nil {
					t.Fatal(err)
				}
				defer dr.Close()
				got, err := io.ReadAll(dr)
				if string(got) != tc.wantOutput {
					t.Fatalf("ReadAll output = %q, want %q", got, tc.wantOutput)
				}
				tc.assertError(t, err)
				n, err := dr.Read(make([]byte, 1))
				if n != 0 {
					t.Fatalf("read after completion returned %d bytes, want 0", n)
				}
				tc.assertError(t, err)
			})
			assertNoTempFiles(t, tmpDir)
		})
	}
}

func setupXZBinary(t *testing.T, script string) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "xz"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)
	return tmpDir
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files were not removed: %v", entries)
	}
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
