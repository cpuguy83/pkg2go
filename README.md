# pkg2go

Read Linux package artifacts — and the archive container formats they are built
from — as a Go [`io/fs.FS`](https://pkg.go.dev/io/fs#FS).

## Idea

rpm and deb are just compressed container archives plus a metadata blob. The
reusable primitives are the *container formats*, so this module implements
`fs.FS` for those and lets the package formats fall out as thin compositions
(a file inside one FS is the source for the next):

- **deb** = an `ar` archive of `control.tar.*` and `data.tar.*`
  → rootfs = `ar` FS → open `data.tar.*` → decompress → `tar` FS
- **rpm** = header metadata + a compressed `cpio` payload
  → rootfs = decompress payload → `cpio` FS

Compressed inner members are decompressed to a temp file and served through an
`io.ReaderAt`, so every container reader is pure random-access indexing over a
`ReaderAt` regardless of the codec.

## Packages

| Package | Status | Description |
|---------|--------|-------------|
| `cpio`  | ✅ initial | newc (SVR4) cpio archives as `fs.FS` |
| `ar`    | ✅ initial | Unix `ar` archives as `fs.FS` |
| `tar`   | ✅ initial | tar as `fs.FS` (thin wrapper) |
| `deb`   | planned | `.deb` packages (ar + tar) |
| `rpm`   | planned | `.rpm` packages (header + cpio) |

## Autodetection

The top-level `pkg2go.NewFS` sniffs the archive magic and dispatches to the
right container reader:

```go
f, _ := os.Open("archive")
info, _ := f.Stat()
fsys, _ := pkg2go.NewFS(f, info.Size()) // ar or cpio, chosen by magic
```

It recognizes `ar` archives and newc `cpio` archives. Compressed streams are not
detected — decompress first and pass the resulting `io.ReaderAt`.

## cpio

```go
f, _ := os.Open("payload.cpio")
info, _ := f.Stat()
fsys, _ := cpio.NewFS(f, info.Size())

data, _ := fs.ReadFile(fsys, "usr/bin/hello")
```

`FS` implements `fs.FS`, `fs.StatFS`, `fs.ReadDirFS`, and `fs.ReadLinkFS`.
Per-entry metadata beyond `fs.FileInfo` (uid/gid, device numbers, link count)
is available via `info.Sys().(cpio.Header)`.

Only the newc formats (`070701` and `070702`) are supported — the formats
produced by `cpio -H newc` and used by rpm payloads.

## ar

```go
f, _ := os.Open("package.deb")
info, _ := f.Stat()
fsys, _ := ar.NewFS(f, info.Size())

control, _ := fs.ReadFile(fsys, "control.tar.gz")
```

`FS` implements `fs.FS`, `fs.StatFS`, and `fs.ReadDirFS`. ar is a flat archive,
so every member is a regular file directly under the root. Per-member metadata
(uid/gid, mode) is available via `info.Sys().(ar.Header)`.

The GNU (`//` name table with `/N` references) and BSD (`#1/N`) extended-name
variants are supported. Archive symbol and name tables are consumed for indexing
but not exposed as members.

## tar

```go
f, _ := os.Open("data.tar")
info, _ := f.Stat()
fsys, _ := tar.NewFS(f, info.Size())

data, _ := fs.ReadFile(fsys, "usr/bin/app")
```

`FS` implements `fs.FS`, `fs.StatFS`, `fs.ReadDirFS`, and `fs.ReadLinkFS`.
Parsing is delegated to `archive/tar`, so GNU/PAX long names and extended
attributes are handled transparently; hard links resolve to their target's data.
Per-entry metadata is available via `info.Sys().(*tar.Header)` (the
`archive/tar` header). GNU sparse files are not supported.
