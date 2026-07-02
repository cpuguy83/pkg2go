package rpm

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Sizes and magic values of the RPM on-disk structures.
const (
	leadSize        = 96
	headerIntroSize = 16
	indexEntrySize  = 16
)

// RPM header tags and types relevant to locating the payload.
const (
	tagPayloadCompressor = 1125 // RPMTAG_PAYLOADCOMPRESSOR
	typeString           = 6    // RPM_STRING_TYPE
)

var (
	leadMagic = []byte{0xed, 0xab, 0xee, 0xdb}
	// The header magic is the 3-byte signature plus the version byte (0x01);
	// the four reserved bytes that follow are always zero.
	headerMagic = []byte{0x8e, 0xad, 0xe8, 0x01}
)

// archive records where the cpio payload begins and how it is compressed.
type archive struct {
	payloadOffset int64
	payloadSize   int64
	compressor    string // RPMTAG_PAYLOADCOMPRESSOR, "" if absent
}

// parse reads the lead and the two headers of the RPM in ra just far enough to
// locate the payload and learn its compressor. The payload itself is not read.
func parse(ra io.ReaderAt, size int64) (*archive, error) {
	if size < leadSize {
		return nil, fmt.Errorf("rpm: file too small to be an RPM package")
	}
	lead := make([]byte, len(leadMagic))
	if _, err := ra.ReadAt(lead, 0); err != nil {
		return nil, err
	}
	if !bytes.Equal(lead, leadMagic) {
		return nil, fmt.Errorf("rpm: not an RPM package: bad lead magic")
	}

	// The signature header is only skipped; its contents are not needed. The
	// main header that follows it starts on the next 8-byte boundary.
	sigLen, _, _, err := headerMeta(ra, leadSize, size)
	if err != nil {
		return nil, fmt.Errorf("rpm: signature header: %w", err)
	}
	mainOffset := leadSize + sigLen
	mainOffset += (8 - (sigLen % 8)) % 8

	// The main header carries the tags; the payload begins immediately after it.
	mainLen, index, store, err := headerFull(ra, mainOffset, size)
	if err != nil {
		return nil, fmt.Errorf("rpm: main header: %w", err)
	}

	payloadOffset := mainOffset + mainLen
	if payloadOffset > size {
		return nil, fmt.Errorf("rpm: payload offset %d is past end of file %d", payloadOffset, size)
	}

	return &archive{
		payloadOffset: payloadOffset,
		payloadSize:   size - payloadOffset,
		compressor:    stringTag(index, store, tagPayloadCompressor),
	}, nil
}

// headerMeta reads a header intro at off and returns the header's total on-disk
// length together with its index and store sizes, without reading the body.
func headerMeta(ra io.ReaderAt, off, size int64) (length int64, nindex, hsize uint32, err error) {
	if off+headerIntroSize > size {
		return 0, 0, 0, fmt.Errorf("truncated header intro")
	}
	intro := make([]byte, headerIntroSize)
	if _, err = ra.ReadAt(intro, off); err != nil {
		return 0, 0, 0, err
	}
	if !bytes.Equal(intro[:len(headerMagic)], headerMagic) {
		return 0, 0, 0, fmt.Errorf("bad header magic")
	}
	nindex = binary.BigEndian.Uint32(intro[8:12])
	hsize = binary.BigEndian.Uint32(intro[12:16])
	length = headerIntroSize + int64(nindex)*indexEntrySize + int64(hsize)
	if length < headerIntroSize || off+length > size {
		return 0, 0, 0, fmt.Errorf("header length %d exceeds file size", length)
	}
	return length, nindex, hsize, nil
}

// headerFull reads a header at off in full, returning its length, index entry
// bytes, and data store bytes.
func headerFull(ra io.ReaderAt, off, size int64) (length int64, index, store []byte, err error) {
	length, nindex, hsize, err := headerMeta(ra, off, size)
	if err != nil {
		return 0, nil, nil, err
	}
	index = make([]byte, int64(nindex)*indexEntrySize)
	if _, err = ra.ReadAt(index, off+headerIntroSize); err != nil {
		return 0, nil, nil, err
	}
	store = make([]byte, hsize)
	if _, err = ra.ReadAt(store, off+headerIntroSize+int64(len(index))); err != nil {
		return 0, nil, nil, err
	}
	return length, index, store, nil
}

// stringTag returns the value of a RPM_STRING_TYPE tag, or "" if it is absent
// or not a string. A string value is a NUL-terminated run of bytes in the data
// store at the entry's offset.
func stringTag(index, store []byte, tag uint32) string {
	for i := 0; i+indexEntrySize <= len(index); i += indexEntrySize {
		e := index[i : i+indexEntrySize]
		if binary.BigEndian.Uint32(e[0:4]) != tag {
			continue
		}
		if binary.BigEndian.Uint32(e[4:8]) != typeString {
			return ""
		}
		offset := int64(binary.BigEndian.Uint32(e[8:12]))
		if offset < 0 || offset >= int64(len(store)) {
			return ""
		}
		s := store[offset:]
		if n := bytes.IndexByte(s, 0); n >= 0 {
			return string(s[:n])
		}
		return string(s)
	}
	return ""
}
