// Package compress decompresses a package member into a random-access
// [io.ReaderAt].
//
// The container readers in this module need random access over an
// [io.ReaderAt], but the compressed inner members of a package are sequential
// streams. [Decompress] expands such a member to a temporary file once and
// serves it back as a [*File], which is an [io.ReaderAt] whose [File.Close]
// removes the backing file. This is uniform across every codec, avoiding
// per-codec seekability handling. An uncompressed member is passed through
// directly — no copy and no temporary file.
//
// The gzip and bzip2 codecs come from the standard library. The xz codec uses
// the system xz binary when available on PATH, otherwise github.com/ulikunitz/xz.
// The lzma codec uses github.com/ulikunitz/xz and zstd uses
// github.com/klauspost/compress/zstd.
package compress

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

// Codec identifies a compression format.
type Codec string

const (
	None  Codec = ""      // no (recognized) compression
	Gzip  Codec = "gzip"  //
	Bzip2 Codec = "bzip2" //
	Xz    Codec = "xz"    //
	Zstd  Codec = "zstd"  //
	Lzma  Codec = "lzma"  //
)

// magic maps a codec to its leading signature. lzma has no reliable magic and is
// therefore only selectable explicitly via [DecompressAs].
var magics = []struct {
	codec Codec
	magic []byte
}{
	{Gzip, []byte{0x1f, 0x8b}},
	{Bzip2, []byte("BZh")},
	{Xz, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
	{Zstd, []byte{0x28, 0xb5, 0x2f, 0xfd}},
}

// maxMagic is the length of the longest signature we need to peek.
const maxMagic = 6

// File is the decompressed content of a package member exposed as a random-access
// [io.ReaderAt]. Compressed members are backed by a temporary file that
// [File.Close] removes; an uncompressed member is served directly from the
// source, in which case [File.Close] is a no-op.
type File struct {
	ra   io.ReaderAt
	size int64
	tmp  *os.File // non-nil only when backed by a temporary file
}

var (
	_ io.ReaderAt = (*File)(nil)
	_ io.Closer   = (*File)(nil)
)

// ReadAt implements [io.ReaderAt].
func (fl *File) ReadAt(p []byte, off int64) (int, error) { return fl.ra.ReadAt(p, off) }

// Size returns the decompressed length in bytes.
func (fl *File) Size() int64 { return fl.size }

// Close removes the backing temporary file, if any. It is a no-op for an
// uncompressed member served directly from its source.
func (fl *File) Close() error {
	if fl.tmp == nil {
		return nil
	}
	name := fl.tmp.Name()
	err := fl.tmp.Close()
	if rmErr := os.Remove(name); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Detect reports the compression codec of ra from its leading magic. A member
// with no recognized signature reports [None].
func Detect(ra io.ReaderAt) (Codec, error) {
	buf := make([]byte, maxMagic)
	n, err := ra.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return None, err
	}
	buf = buf[:n]
	for _, m := range magics {
		if bytes.HasPrefix(buf, m.magic) {
			return m.codec, nil
		}
	}
	return None, nil
}

// Decompress detects the codec of the size-byte member ra from its leading magic
// and expands it to a temporary file, returning the result as a random-access
// [*File]. An uncompressed member is passed through directly, without copying.
// The caller must [File.Close] the result to release any backing file.
func Decompress(ra io.ReaderAt, size int64) (*File, error) {
	codec, err := Detect(ra)
	if err != nil {
		return nil, err
	}
	return DecompressAs(ra, size, codec)
}

// DecompressAs expands the size-byte member ra using the named codec, returning
// the result as a random-access [*File]. [None] passes the member through
// directly, without copying. The caller must [File.Close] the result to release
// any backing file.
func DecompressAs(ra io.ReaderAt, size int64, codec Codec) (*File, error) {
	if codec == None {
		return &File{ra: io.NewSectionReader(ra, 0, size), size: size}, nil
	}
	dr, err := decoder(io.NewSectionReader(ra, 0, size), codec)
	if err != nil {
		return nil, err
	}
	defer dr.Close()
	return spool(dr)
}

// decoder wraps r in the decompressor for codec. Callers must close the reader,
// including when reading stops before EOF.
func decoder(r io.Reader, codec Codec) (io.ReadCloser, error) {
	switch codec {
	case Gzip:
		return gzip.NewReader(r)
	case Bzip2:
		return io.NopCloser(bzip2.NewReader(r)), nil
	case Xz:
		if xzBinaryPath, err := exec.LookPath("xz"); err == nil {
			return newXZBinaryReader(xzBinaryPath, r), nil
		}
		xr, err := xz.NewReader(r)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(xr), nil
	case Zstd:
		zr, err := zstd.NewReader(r)
		if err != nil {
			return nil, err
		}
		return zr.IOReadCloser(), nil
	case Lzma:
		lr, err := lzma.NewReader(r)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(lr), nil
	default:
		return nil, fmt.Errorf("compress: unknown codec %q", codec)
	}
}

// spool copies dr into a fresh temporary file and returns it as a [*File].
func spool(dr io.Reader) (*File, error) {
	tmp, err := os.CreateTemp("", "pkg2go-*")
	if err != nil {
		return nil, err
	}

	n, err := io.Copy(tmp, dr)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return &File{ra: tmp, size: n, tmp: tmp}, nil
}

type xzBinaryReader struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr bytes.Buffer
	err    error
	closed bool
}

func newXZBinaryReader(xzBinaryPath string, r io.Reader) *xzBinaryReader {
	cmd := exec.Command(xzBinaryPath, "--decompress", "--stdout", "--format=xz")
	xr := &xzBinaryReader{cmd: cmd}
	cmd.Stdin = r
	cmd.Stderr = &xr.stderr
	// Host compression preferences must not change library behavior.
	envs := cmd.Environ()
	cmd.Env = []string{}
	for _, env := range envs {
		if !strings.HasPrefix(env, "XZ_DEFAULTS=") && !strings.HasPrefix(env, "XZ_OPT=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	return xr
}

func (r *xzBinaryReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, os.ErrClosed
	}
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.stdout == nil {
		r.stdout, r.err = r.cmd.StdoutPipe()
		if r.err == nil {
			r.err = r.cmd.Start()
		}
		if r.err != nil {
			r.err = fmt.Errorf("compress: xz: %w", r.err)
			return 0, r.err
		}
	}
	n, err := r.stdout.Read(p)
	if err == io.EOF {
		// EOF is only reported after the process has exited successfully.
		if waitErr := r.cmd.Wait(); waitErr != nil {
			err = fmt.Errorf("compress: xz: %w: %s", waitErr, strings.TrimSpace(r.stderr.String()))
		}
	}
	r.err = err
	return n, err
}

// Close also stops and reaps the process if spooling fails before reaching EOF.
func (r *xzBinaryReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.cmd.Process == nil || r.cmd.ProcessState != nil {
		return nil
	}
	killErr := r.cmd.Process.Kill()
	waitErr := r.cmd.Wait()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("compress: stop xz: %w", killErr)
	}
	// Terminating an unfinished decoder intentionally produces an exit error.
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return fmt.Errorf("compress: wait for xz: %w", waitErr)
	}
	return nil
}
