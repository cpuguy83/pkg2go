// Package ar reads Unix "ar" archives and exposes their members as an
// [io/fs.FS].
//
// ar is the outer container of a Debian package: a .deb is an ar archive whose
// members are debian-binary, control.tar.* and data.tar.*. The format is a flat
// list of members with no directories and no symlinks, so [FS] presents every
// member as a regular file directly under the root.
//
// An ar archive is a sequential stream. To satisfy the random-access
// [io/fs.FS] contract, [NewFS] indexes the archive once over an [io.ReaderAt]
// and then serves reads by seeking back into the source.
//
// The GNU and BSD extended-name variants are supported: the GNU "//" name table
// with "/N" references and BSD "#1/N" names whose real name is stored at the
// front of the member data. Archive symbol and name tables (GNU "/" and "//",
// BSD "__.SYMDEF") are consumed for indexing but are not exposed as members.
//
// FS implements [fs.FS], [fs.StatFS], and [fs.ReadDirFS]. Per-member metadata
// beyond [io/fs.FileInfo] (owner, group, raw mode) is available through
// [fs.FileInfo.Sys], which returns a [Header].
package ar
