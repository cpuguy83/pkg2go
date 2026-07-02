// Package deb reads a Debian .deb package.
//
// A .deb is an ar archive with three members: a debian-binary member holding
// the format version, a control.tar.* member with the package metadata, and a
// data.tar.* member with the files the package installs. This package composes
// the container readers in this module rather than reimplementing any of them:
// the ar archive is read by [github.com/cpuguy83/pkg2go/ar], its tar members are
// decompressed by [github.com/cpuguy83/pkg2go/compress] and read by
// github.com/cpuguy83/tar2go.
//
// [Open] indexes only the outer ar archive; the data member is decompressed
// lazily on first use by [Package.Rootfs] and memoized. The temporary files
// backing any compressed members are released by [Package.Close].
package deb
