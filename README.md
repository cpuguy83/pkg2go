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
| `ar`    | planned | Unix `ar` archives as `fs.FS` |
| `tar`   | planned | tar as `fs.FS` (thin wrapper) |
| `deb`   | planned | `.deb` packages (ar + tar) |
| `rpm`   | planned | `.rpm` packages (header + cpio) |

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
