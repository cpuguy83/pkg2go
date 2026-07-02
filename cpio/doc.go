// Package cpio reads cpio archives and exposes their contents as an [io/fs.FS].
//
// Only the "new ASCII" (SVR4) formats are supported: "070701" (newc) and
// "070702" (newc with CRC). These are the formats produced by GNU cpio's
// -H newc option and used by RPM package payloads. The older binary, "odc"
// (070707), and portable ASCII formats are not supported.
//
// A cpio archive is a flat, sequential list of entries. To satisfy the
// random-access [io/fs.FS] contract, [NewFS] indexes the archive once over an
// [io.ReaderAt] and then serves reads by seeking back into the source. Missing
// parent directories are synthesized so that walking the tree behaves like a
// real filesystem.
package cpio
