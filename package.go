package pkg2go

import (
	"fmt"
	"io"
	"io/fs"

	"github.com/cpuguy83/pkg2go/deb"
	"github.com/cpuguy83/pkg2go/rpm"
)

// rpmMagic is the four-byte magic at the start of an RPM lead.
const rpmMagic = "\xed\xab\xee\xdb"

// Package is an opened OS package whose installed files can be read as an
// [io/fs.FS]. Both [*deb.Package] and [*rpm.Package] satisfy it.
type Package interface {
	// Rootfs returns the files the package installs as an fs.FS rooted at the
	// filesystem root.
	Rootfs() (fs.FS, error)
	// Close releases any temporary files created while reading the package.
	Close() error
}

// OpenPackage identifies whether ra, which must contain exactly size bytes, is a
// Debian or RPM package by its magic and returns it as a [Package]. A .deb is
// recognized by its ar magic and read by [deb.Open]; a .rpm is recognized by
// its lead magic and read by [rpm.Open].
//
// It returns an error if the format is not recognized. The returned Package
// must be closed.
func OpenPackage(ra io.ReaderAt, size int64) (Package, error) {
	buf := make([]byte, len(arMagic))
	n, err := ra.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	switch {
	case hasPrefix(buf, arMagic):
		p, err := deb.Open(ra, size)
		if err != nil {
			return nil, err
		}
		return p, nil
	case hasPrefix(buf, rpmMagic):
		p, err := rpm.Open(ra, size)
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("pkg2go: unrecognized package format")
	}
}
