package cpio

import (
	"fmt"
	"io/fs"
	"time"
)

// newc header layout: a 6-byte magic followed by thirteen 8-byte, zero-padded,
// big-endian hexadecimal fields. The fixed header is therefore 110 bytes.
const (
	headerSize = 110

	magicNewc    = "070701" // new ASCII
	magicNewcCRC = "070702" // new ASCII with CRC

	// trailerName marks the end of the archive.
	trailerName = "TRAILER!!!"
)

// POSIX mode type bits (st_mode & S_IFMT). cpio stores the full st_mode; these
// let us recover the file type and translate it to an [io/fs.FileMode].
const (
	sIFMT   = 0o170000
	sIFSOCK = 0o140000
	sIFLNK  = 0o120000
	sIFREG  = 0o100000
	sIFBLK  = 0o060000
	sIFDIR  = 0o040000
	sIFCHR  = 0o020000
	sIFIFO  = 0o010000

	sISUID = 0o4000
	sISGID = 0o2000
	sISVTX = 0o1000
)

// Header holds the metadata for a single cpio entry. It is exposed through
// [fs.FileInfo.Sys] so callers can recover ownership, device numbers, and other
// fields that [io/fs.FileInfo] does not carry.
type Header struct {
	Name      string      // path as stored in the archive (uncleaned)
	Ino       int64       // inode number
	Mode      fs.FileMode // permission bits plus type bits
	UID       int         // owner user id
	GID       int         // owner group id
	Links     int         // hard link count
	ModTime   time.Time   // modification time
	Size      int64       // size of the file data in bytes
	DevMajor  int64       // device major, for the file itself
	DevMinor  int64       // device minor, for the file itself
	RdevMajor int64       // device major, for character/block devices
	RdevMinor int64       // device minor, for character/block devices
	Linkname  string      // symlink target, populated for symbolic links
}

// rawHeader is the decoded fixed header, before the name and data are read.
type rawHeader struct {
	ino       int64
	mode      uint32
	uid       int
	gid       int
	links     int
	mtime     int64
	size      int64
	devMajor  int64
	devMinor  int64
	rdevMajor int64
	rdevMinor int64
	nameSize  int
	crc       bool
}

// parseHeader decodes a 110-byte newc fixed header.
func parseHeader(b []byte) (rawHeader, error) {
	if len(b) < headerSize {
		return rawHeader{}, fmt.Errorf("cpio: short header: got %d bytes, want %d", len(b), headerSize)
	}

	magic := string(b[0:6])
	var crc bool
	switch magic {
	case magicNewc:
	case magicNewcCRC:
		crc = true
	default:
		return rawHeader{}, fmt.Errorf("cpio: unsupported magic %q (only newc 070701/070702 are supported)", magic)
	}

	fields, err := parseHexFields(b[6:headerSize])
	if err != nil {
		return rawHeader{}, err
	}

	return rawHeader{
		ino:       int64(fields[0]),
		mode:      fields[1],
		uid:       int(fields[2]),
		gid:       int(fields[3]),
		links:     int(fields[4]),
		mtime:     int64(fields[5]),
		size:      int64(fields[6]),
		devMajor:  int64(fields[7]),
		devMinor:  int64(fields[8]),
		rdevMajor: int64(fields[9]),
		rdevMinor: int64(fields[10]),
		nameSize:  int(fields[11]),
		crc:       crc,
	}, nil
}

// parseHexFields decodes 13 consecutive 8-character ASCII hex fields.
func parseHexFields(b []byte) ([13]uint32, error) {
	var out [13]uint32
	if len(b) < 13*8 {
		return out, fmt.Errorf("cpio: short header fields: got %d bytes, want %d", len(b), 13*8)
	}
	for i := range out {
		v, err := parseHex8(b[i*8 : i*8+8])
		if err != nil {
			return out, err
		}
		out[i] = v
	}
	return out, nil
}

// parseHex8 decodes exactly 8 ASCII hex digits into a uint32.
func parseHex8(b []byte) (uint32, error) {
	var v uint32
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint32(c-'A') + 10
		default:
			return 0, fmt.Errorf("cpio: invalid hex digit %q in header", string(rune(c)))
		}
	}
	return v, nil
}

// toHeader converts the raw fixed header plus name into the exported Header.
func (h rawHeader) toHeader(name string) Header {
	return Header{
		Name:      name,
		Ino:       h.ino,
		Mode:      modeFromCpio(h.mode),
		UID:       h.uid,
		GID:       h.gid,
		Links:     h.links,
		ModTime:   time.Unix(h.mtime, 0),
		Size:      h.size,
		DevMajor:  h.devMajor,
		DevMinor:  h.devMinor,
		RdevMajor: h.rdevMajor,
		RdevMinor: h.rdevMinor,
	}
}

// modeFromCpio translates a POSIX st_mode into an [io/fs.FileMode].
func modeFromCpio(m uint32) fs.FileMode {
	mode := fs.FileMode(m & 0o777)
	switch m & sIFMT {
	case sIFDIR:
		mode |= fs.ModeDir
	case sIFLNK:
		mode |= fs.ModeSymlink
	case sIFCHR:
		mode |= fs.ModeCharDevice | fs.ModeDevice
	case sIFBLK:
		mode |= fs.ModeDevice
	case sIFIFO:
		mode |= fs.ModeNamedPipe
	case sIFSOCK:
		mode |= fs.ModeSocket
	case sIFREG:
		// no type bits
	}
	if m&sISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if m&sISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if m&sISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return mode
}

// align4 rounds n up to the next multiple of four. newc pads the header+name
// and the file data to four-byte boundaries.
func align4(n int64) int64 {
	return (n + 3) &^ 3
}
