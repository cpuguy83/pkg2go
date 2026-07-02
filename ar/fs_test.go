package ar

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"testing/fstest"
)

func TestNewFS(t *testing.T) {
	t.Run("a short-named member is readable through Open", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "debian-binary", mode: 0o644, data: []byte("2.0\n")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "debian-binary")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "2.0\n" {
			t.Fatalf("content = %q, want %q", got, "2.0\n")
		}
	})

	t.Run("a short name has its trailing slash terminator stripped", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "control.tar", mode: 0o644, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		if _, err := fs.Stat(fsys, "control.tar"); err != nil {
			t.Fatalf("Stat control.tar: %v", err)
		}
		if _, err := fs.Stat(fsys, "control.tar/"); err == nil {
			t.Fatal("Stat control.tar/: got nil error, want error")
		}
	})

	t.Run("a name longer than fifteen bytes is resolved from the name table", func(t *testing.T) {
		long := "this-is-a-very-long-member-name-over-16.txt"
		archive := writeAr(t, []member{
			{name: long, mode: 0o644, data: []byte("aaa")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, long)
		if err != nil {
			t.Fatalf("ReadFile %q: %v", long, err)
		}
		if string(got) != "aaa" {
			t.Fatalf("content = %q, want %q", got, "aaa")
		}
	})

	t.Run("a BSD #1/N name is resolved from the member data prefix", func(t *testing.T) {
		archive := writeBSDMember(t, "long-bsd-name.txt", []byte("payload"))

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "long-bsd-name.txt")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(got) != "payload" {
			t.Fatalf("content = %q, want %q", got, "payload")
		}
	})

	t.Run("the GNU symbol table member is not listed", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString(magic)
		writeRawMember(&buf, gnuSymbolTable, []byte("symtab"), 0, 0, 0)
		writeRawMember(&buf, "real.o/", []byte("obj"), 0o644, 0, 0)

		fsys := newFS(t, buf.Bytes())

		names := dirNames(t, fsys)
		if want := []string{"real.o"}; !equalStrings(names, want) {
			t.Fatalf("members = %v, want %v", names, want)
		}
	})

	t.Run("the GNU name table member is not listed", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "another-long-name-also-exceeding-sixteen.txt", mode: 0o644, data: []byte("b")},
		})

		fsys := newFS(t, archive)

		for _, n := range dirNames(t, fsys) {
			if n == gnuNameTable || n == gnuSymbolTable {
				t.Fatalf("name table member %q was listed", n)
			}
		}
	})

	t.Run("an odd-sized member is followed by the next member on an even boundary", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "odd", mode: 0o644, data: []byte("abc")}, // 3 bytes -> 1 pad byte
			{name: "next", mode: 0o644, data: []byte("second")},
		})

		fsys := newFS(t, archive)

		got, err := fs.ReadFile(fsys, "next")
		if err != nil {
			t.Fatalf("ReadFile next: %v", err)
		}
		if string(got) != "second" {
			t.Fatalf("content = %q, want %q", got, "second")
		}
	})

	t.Run("owner, group and mode are exposed through the Header in Sys", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "owned", mode: 0o640, uid: 1000, gid: 2000, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "owned")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		hdr, ok := info.Sys().(Header)
		if !ok {
			t.Fatalf("Sys() type = %T, want ar.Header", info.Sys())
		}
		if hdr.UID != 1000 || hdr.GID != 2000 {
			t.Fatalf("uid/gid = %d/%d, want 1000/2000", hdr.UID, hdr.GID)
		}
		if hdr.Mode.Perm() != 0o640 {
			t.Fatalf("mode = %o, want 640", hdr.Mode.Perm())
		}
	})

	t.Run("a member reports as a regular file, not a directory", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "data.tar", mode: 0o644, data: []byte("x")},
		})

		fsys := newFS(t, archive)

		info, err := fs.Stat(fsys, "data.tar")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.IsDir() {
			t.Fatal("IsDir = true, want false")
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("mode = %v, want regular", info.Mode())
		}
	})

	t.Run("a missing member reports fs.ErrNotExist", func(t *testing.T) {
		fsys := newFS(t, writeAr(t, nil))

		_, err := fsys.Open("nope")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("a .deb-style member set lists debian-binary, control and data", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "debian-binary", mode: 0o644, data: []byte("2.0\n")},
			{name: "control.tar.gz", mode: 0o644, data: []byte("control")},
			{name: "data.tar.gz", mode: 0o644, data: []byte("data")},
		})

		fsys := newFS(t, archive)

		names := dirNames(t, fsys)
		want := []string{"control.tar.gz", "data.tar.gz", "debian-binary"}
		if !equalStrings(names, want) {
			t.Fatalf("members = %v, want %v", names, want)
		}
	})

	t.Run("it satisfies the io/fs contract", func(t *testing.T) {
		archive := writeAr(t, []member{
			{name: "debian-binary", mode: 0o644, data: []byte("2.0\n")},
			{name: "control.tar.gz", mode: 0o644, data: []byte("control payload")},
			{name: "a-name-longer-than-sixteen-bytes.bin", mode: 0o644, data: []byte("long")},
		})

		fsys := newFS(t, archive)

		if err := fstest.TestFS(fsys, "debian-binary", "control.tar.gz", "a-name-longer-than-sixteen-bytes.bin"); err != nil {
			t.Fatalf("TestFS: %v", err)
		}
	})
}

func TestNewFSErrors(t *testing.T) {
	t.Run("a stream without the global magic is rejected", func(t *testing.T) {
		bad := []byte("not an ar archive at all")

		_, err := NewFS(bytes.NewReader(bad), int64(len(bad)))
		if err == nil {
			t.Fatal("got nil error, want bad-magic error")
		}
	})

	t.Run("a member size past the end of the archive is rejected", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString(magic)
		// Claim a size far larger than the data that follows.
		buf.Write(arHeader("truncated/", 4096, 0o644, 0, 0))
		buf.WriteString("short")

		_, err := NewFS(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err == nil {
			t.Fatal("got nil error, want truncated-member error")
		}
	})
}

// TestAgainstSystemAr validates the reader against archives produced by the
// system ar binary. It is skipped where ar is unavailable so the suite stays
// hermetic.
func TestAgainstSystemAr(t *testing.T) {
	arBin, err := exec.LookPath("ar")
	if err != nil {
		t.Skip("system ar not available")
	}

	files := map[string]string{
		"debian-binary":                        "2.0\n",
		"control.tar":                          "control payload\n",
		"data.tar":                             "data payload contents\n",
		"a-name-longer-than-sixteen-bytes.bin": "long name content\n",
	}

	dir := t.TempDir()
	names := make([]string, 0, len(files))
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	archive := filepath.Join(dir, "test.a")
	cmd := exec.Command(arBin, append([]string{"rc", archive}, names...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ar rc: %v: %s", err, out)
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

	got := dirNames(t, fsys)
	if !equalStrings(got, names) {
		t.Fatalf("members = %v, want %v", got, names)
	}
	for name, content := range files {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if string(data) != content {
			t.Fatalf("%s content = %q, want %q", name, data, content)
		}
	}

	if err := fstest.TestFS(fsys, names...); err != nil {
		t.Fatalf("TestFS: %v", err)
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

// member describes a single archive member for the test writer.
type member struct {
	name string
	mode uint32
	uid  int
	gid  int
	data []byte
}

// writeAr builds a GNU-style ar archive, emitting a "//" name table for any
// member whose name is too long for the 16-byte name field, exactly as GNU ar
// does.
func writeAr(t *testing.T, members []member) []byte {
	t.Helper()

	var table bytes.Buffer
	offsets := make(map[int]int, len(members))
	for i, m := range members {
		if len(m.name) > nameLen-1 { // name + "/" must fit in 16 bytes
			offsets[i] = table.Len()
			table.WriteString(m.name + "/\n")
		}
	}

	var buf bytes.Buffer
	buf.WriteString(magic)
	if table.Len() > 0 {
		writeRawMember(&buf, gnuNameTable, table.Bytes(), 0, 0, 0)
	}
	for i, m := range members {
		nameField := m.name + "/"
		if off, ok := offsets[i]; ok {
			nameField = "/" + strconv.Itoa(off)
		}
		writeRawMember(&buf, nameField, m.data, m.mode, m.uid, m.gid)
	}
	return buf.Bytes()
}

// writeBSDMember builds a single-member archive using the BSD "#1/N" extended
// name encoding, where the real name is stored at the front of the member data.
func writeBSDMember(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(magic)
	payload := append([]byte(name), data...)
	writeRawMember(&buf, "#1/"+strconv.Itoa(len(name)), payload, 0o644, 0, 0)
	return buf.Bytes()
}

// writeRawMember writes one member: header, data, and a newline pad byte when
// the data length is odd. nameField is written verbatim into the name column.
func writeRawMember(w *bytes.Buffer, nameField string, data []byte, mode uint32, uid, gid int) {
	w.Write(arHeader(nameField, int64(len(data)), mode, uid, gid))
	w.Write(data)
	if len(data)%2 == 1 {
		w.WriteByte('\n')
	}
}

// arHeader formats a 60-byte member header with the given name field and size.
func arHeader(nameField string, size int64, mode uint32, uid, gid int) []byte {
	b := make([]byte, headerSize)
	for i := range b {
		b[i] = ' '
	}
	put := func(off, length int, s string) {
		if len(s) > length {
			s = s[:length]
		}
		copy(b[off:off+length], s)
	}
	put(nameOff, nameLen, nameField)
	put(mtimeOff, mtimeLen, "0")
	put(uidOff, uidLen, strconv.Itoa(uid))
	put(gidOff, gidLen, strconv.Itoa(gid))
	put(modeOff, modeLen, strconv.FormatUint(uint64(mode), 8))
	put(sizeOff, sizeLen, strconv.FormatInt(size, 10))
	copy(b[termOff:termOff+termLen], fieldTerminator)
	return b
}

// dirNames returns the sorted member names at the root of fsys.
func dirNames(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
