// Package tar reads tar archives and exposes their contents as an [io/fs.FS].
//
// A tar archive is a flat, sequential list of entries. To satisfy the
// random-access [io/fs.FS] contract, [NewFS] indexes the archive once over an
// [io.ReaderAt] and then serves reads by seeking back into the source. Missing
// parent directories are synthesized so that walking the tree behaves like a
// real filesystem.
//
// Parsing is delegated to [archive/tar], so the GNU and PAX extensions it
// understands (long names, extended attributes, and so on) are handled
// transparently. Hard links are resolved to the data of their target.
//
// FS implements [fs.FS], [fs.StatFS], [fs.ReadDirFS], and [fs.ReadLinkFS].
// Per-entry metadata beyond [io/fs.FileInfo] is available through
// [fs.FileInfo.Sys], which returns the underlying [archive/tar.Header].
//
// GNU sparse files are not supported for random access and are rejected by
// [NewFS].
package tar
