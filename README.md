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
| `compress` | ✅ initial | decompress a stream to a random-access `io.ReaderAt` |
| `deb`   | planned | `.deb` packages (ar + tar) |
| `rpm`   | planned | `.rpm` packages (header + cpio) |

tar is read via [`github.com/cpuguy83/tar2go`](https://github.com/cpuguy83/tar2go)
rather than a reader in this module.

## Autodetection

The top-level `pkg2go.NewFS` sniffs the archive magic and dispatches to the
right container reader:

```go
f, _ := os.Open("archive")
info, _ := f.Stat()
fsys, _ := pkg2go.NewFS(f, info.Size()) // ar or cpio, chosen by magic
```

It recognizes `ar` archives, newc `cpio` archives, and `tar` archives (via
tar2go). Compressed streams are not detected — decompress first and pass the
resulting `io.ReaderAt`.

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

tar is read through [`github.com/cpuguy83/tar2go`](https://github.com/cpuguy83/tar2go),
which indexes a tar over an `io.ReaderAt` and serves it as an `fs.FS`:

```go
f, _ := os.Open("data.tar")
fsys := tar2go.NewIndex(f).FS()

data, _ := fs.ReadFile(fsys, "usr/bin/app")
```

`pkg2go.NewFS` wires this up automatically when it detects a tar stream.

## compress

```go
f, _ := os.Open("data.tar.zst")
info, _ := f.Stat()
dec, _ := compress.Decompress(f, info.Size()) // sniffs the codec by magic
defer dec.Close()

fsys := tar2go.NewIndex(io.NewSectionReader(dec, 0, dec.Size())).FS()
```

`Decompress` takes an `io.ReaderAt` and its size, detects the codec from the
leading magic (gzip, bzip2, xz, zstd) and expands the member to a temporary
file, returned as a `*File` that is an `io.ReaderAt` — giving the random access
the container readers need. An uncompressed member is passed through directly,
without copying or a temp file (and `Close` is then a no-op). `DecompressAs`
selects the codec explicitly (needed for lzma, which has no reliable magic; rpm
names its payload codec in the header).
