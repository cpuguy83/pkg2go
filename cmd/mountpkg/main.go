// Command mountpkg FUSE-mounts the root filesystem of a .deb or .rpm package.
//
// Usage:
//
//	mountpkg <package> <mountpoint>
//
// The package type is detected from its magic. The mount is read-only and is
// torn down on SIGINT/SIGTERM.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cpuguy83/go2fuse"
	"github.com/cpuguy83/pkg2go"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: %s <package> <mountpoint>", os.Args[0])
	}
	pkgPath, mountpoint := args[0], args[1]

	f, err := os.Open(pkgPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	pkg, err := pkg2go.OpenPackage(f, info.Size())
	if err != nil {
		return err
	}
	defer pkg.Close()

	rootfs, err := pkg.Rootfs()
	if err != nil {
		return err
	}

	sec := time.Second
	server, err := go2fuse.Mount(mountpoint, rootfs, &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: "pkg2go",
			Name:   "pkg2go",
		},
		EntryTimeout: &sec,
		AttrTimeout:  &sec,
	})
	if err != nil {
		return err
	}

	fmt.Printf("mounted %s at %s (ctrl-c to unmount)\n", pkgPath, mountpoint)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		if err := server.Unmount(); err != nil {
			log.Printf("unmount: %v", err)
		}
	}()

	server.Wait()
	return nil
}
