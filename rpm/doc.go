// Package rpm reads an RPM package.
//
// An .rpm file is a 96-byte lead, a signature header, a main header, and a
// compressed cpio payload. This package parses the lead and the two headers
// only far enough to locate the payload and learn its compression, then
// composes the container readers in this module rather than reimplementing
// them: the payload is decompressed by [github.com/cpuguy83/pkg2go/compress]
// and read by [github.com/cpuguy83/pkg2go/cpio].
//
// [Open] parses the headers eagerly but does not decompress the payload; the
// payload is expanded on first use by [Package.Rootfs] and memoized. The
// temporary file backing the decompressed payload is released by
// [Package.Close].
package rpm
