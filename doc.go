// Package pkg2go reads Linux package artifacts and the container archive
// formats they are built from as an [io/fs.FS].
//
// The subpackages implement individual formats: [github.com/cpuguy83/pkg2go/ar]
// for Unix ar archives (the outer container of a .deb) and
// [github.com/cpuguy83/pkg2go/cpio] for newc cpio archives (used by rpm
// payloads). [NewFS] sniffs the archive magic and dispatches to the right one.
package pkg2go
