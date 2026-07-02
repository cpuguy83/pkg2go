package ar

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"
)

// ar member header layout. All fields are left-justified, space-padded ASCII.
// The fixed header is 60 bytes and is followed immediately by the member data.
const (
	magic      = "!<arch>\n" // global archive magic
	headerSize = 60

	// fieldTerminator ends every member header.
	fieldTerminator = "`\n"
)

// Field offsets within the 60-byte member header.
const (
	nameOff  = 0
	nameLen  = 16
	mtimeOff = 16
	mtimeLen = 12
	uidOff   = 28
	uidLen   = 6
	gidOff   = 34
	gidLen   = 6
	modeOff  = 40
	modeLen  = 8
	sizeOff  = 48
	sizeLen  = 10
	termOff  = 58
	termLen  = 2
)

// Special member names used by the GNU and BSD variants. These carry archive
// metadata rather than real content and are never exposed through the FS.
const (
	gnuSymbolTable = "/"  // GNU symbol table (ar s)
	gnuNameTable   = "//" // GNU extended-name table
	bsdSymbolTable = "__.SYMDEF"
)

// Header holds the metadata for a single ar member. It is exposed through
// [fs.FileInfo.Sys] so callers can recover ownership and the raw mode, which
// [io/fs.FileInfo] does not fully carry.
type Header struct {
	Name    string      // member name, resolved and cleaned of ar terminators
	ModTime time.Time   // modification time
	UID     int         // owner user id
	GID     int         // owner group id
	Mode    fs.FileMode // permission bits (ar stores no file-type bits)
	Size    int64       // size of the member data in bytes
}

// rawHeader is a decoded fixed header before extended names are resolved. name
// is the raw 16-byte name field trimmed of trailing spaces; it may still be a
// GNU "/N" reference or a BSD "#1/N" marker.
type rawHeader struct {
	name    string
	mtime   int64
	uid     int
	gid     int
	mode    uint32
	size    int64
	nameLen int64 // BSD "#1/N": length of the real name stored in the data
}

// parseHeader decodes a 60-byte ar member header.
func parseHeader(b []byte) (rawHeader, error) {
	if len(b) < headerSize {
		return rawHeader{}, fmt.Errorf("ar: short header: got %d bytes, want %d", len(b), headerSize)
	}
	if term := string(b[termOff : termOff+termLen]); term != fieldTerminator {
		return rawHeader{}, fmt.Errorf("ar: invalid header terminator %q", term)
	}

	mtime, err := parseDec(b[mtimeOff:mtimeOff+mtimeLen], "mtime")
	if err != nil {
		return rawHeader{}, err
	}
	uid, err := parseDec(b[uidOff:uidOff+uidLen], "uid")
	if err != nil {
		return rawHeader{}, err
	}
	gid, err := parseDec(b[gidOff:gidOff+gidLen], "gid")
	if err != nil {
		return rawHeader{}, err
	}
	mode, err := parseOctal(b[modeOff : modeOff+modeLen])
	if err != nil {
		return rawHeader{}, err
	}
	size, err := parseDec(b[sizeOff:sizeOff+sizeLen], "size")
	if err != nil {
		return rawHeader{}, err
	}

	rh := rawHeader{
		name:  trimField(b[nameOff : nameOff+nameLen]),
		mtime: mtime,
		uid:   int(uid),
		gid:   int(gid),
		mode:  mode,
		size:  size,
	}

	if n, ok := bsdNameLen(rh.name); ok {
		rh.nameLen = n
	}
	return rh, nil
}

// toHeader builds the exported Header from the raw header and a resolved name.
func (h rawHeader) toHeader(name string) Header {
	return Header{
		Name:    name,
		ModTime: time.Unix(h.mtime, 0),
		UID:     h.uid,
		GID:     h.gid,
		Mode:    fs.FileMode(h.mode & 0o777),
		Size:    h.size,
	}
}

// bsdNameLen reports whether name is a BSD "#1/N" extended-name marker and, if
// so, the length N of the real name stored at the front of the member data.
func bsdNameLen(name string) (int64, bool) {
	const prefix = "#1/"
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	n, err := strconv.ParseInt(name[len(prefix):], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// gnuNameRef reports whether name is a GNU "/N" reference into the extended-name
// table and, if so, the byte offset N.
func gnuNameRef(name string) (int, bool) {
	if len(name) < 2 || name[0] != '/' {
		return 0, false
	}
	off, err := strconv.Atoi(name[1:])
	if err != nil || off < 0 {
		return 0, false
	}
	return off, true
}

// resolveTableName returns the long name stored at offset off in the GNU
// extended-name table. Names are terminated by "/\n" (a trailing "/" then a
// newline); either terminator ends the name.
func resolveTableName(table []byte, off int) (string, error) {
	if off < 0 || off >= len(table) {
		return "", fmt.Errorf("ar: name table offset %d out of range", off)
	}
	end := off
	for end < len(table) && table[end] != '\n' {
		end++
	}
	name := string(table[off:end])
	return strings.TrimSuffix(name, "/"), nil
}

// cleanShortName strips the trailing "/" terminator GNU ar appends to a short
// member name.
func cleanShortName(name string) string {
	return strings.TrimSuffix(name, "/")
}

// isSymbolTable reports whether name is a GNU or BSD archive symbol table, which
// is never exposed as a member.
func isSymbolTable(name string) bool {
	return name == gnuSymbolTable ||
		name == bsdSymbolTable ||
		strings.HasPrefix(name, bsdSymbolTable+" ") // "__.SYMDEF SORTED"
}

// parseDec parses a space-padded decimal field. An empty field is zero, matching
// GNU ar's treatment of unset numeric fields.
func parseDec(b []byte, field string) (int64, error) {
	s := strings.TrimRight(string(b), " ")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ar: invalid %s field %q", field, s)
	}
	return v, nil
}

// parseOctal parses a space-padded octal mode field.
func parseOctal(b []byte) (uint32, error) {
	s := strings.TrimRight(string(b), " ")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("ar: invalid mode field %q", s)
	}
	return uint32(v), nil
}

// trimField trims trailing spaces from a fixed-width header field.
func trimField(b []byte) string {
	return strings.TrimRight(string(b), " ")
}

// align2 rounds n up to the next even boundary. ar pads member data with a
// single newline when the size is odd.
func align2(n int64) int64 {
	return n + n&1
}
