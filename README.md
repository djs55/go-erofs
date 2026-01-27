# go-erofs

A Go library for opening erofs files as a Go stdlib [fs.FS](https://pkg.go.dev/io/fs#FS).

## Scope

This library is designed to allow erofs files to be usable in any Go operation that uses
the standard filesystem interface. This could be useful for accessing an erofs file just
as you would a plain directory without needing to unpack. In the future this library
could provide an interface to create erofs files as well.

## Current state

- [x] Read erofs files created with default `mkfs.erofs` options
- [x] Read chunk-based erofs files (without indexes)
- [x] Xattr support
- [x] Creating erofs files from tar archives
- [x] Drop-in replacement for `mkfs.erofs` binary
- [x] Linux kernel compatibility testing
- [ ] Long xattr prefix support
- [ ] Read erofs files with compression
- [ ] Extra devices for chunked data and chunk indexes
- [ ] Inline data layouts (FLAT_INLINE)
- [ ] Shared xattr support

## Example use

Print out all the files in an erofs file

```
package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/erofs/go-erofs"
)

func main() {
	f, err := os.Open("testdata/basic-default.erofs")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	img, err := erofs.EroFS(f)
	if err != nil {
		log.Fatal(err)
	}

	fs.WalkDir(img, "/", func(path string, entry fs.DirEntry, err error) error {
		fmt.Println(path)
		return nil
	})
}
```

## Creating EROFS files

Convert a tar archive to EROFS:

```go
package main

import (
	"log"
	"os"

	"github.com/erofs/go-erofs"
)

func main() {
	tarFile, err := os.Open("input.tar")
	if err != nil {
		log.Fatal(err)
	}
	defer tarFile.Close()

	erofsFile, err := os.Create("output.erofs")
	if err != nil {
		log.Fatal(err)
	}
	defer erofsFile.Close()

	if err := erofs.MakeFromTar(tarFile, erofsFile); err != nil {
		log.Fatal(err)
	}
}
```

Or use the `mkfs.erofs` drop-in replacement:

```bash
# Convert tar to EROFS
./mkfs.erofs --tar=f output.erofs input.tar

# With options (compatible with mkfs.erofs)
./mkfs.erofs --tar=f --force-uid=0 --force-gid=0 -U uuid output.erofs input.tar
```

## Testing

Run all tests:
```bash
go test ./...
```

Run tests including Linux kernel compatibility tests (requires Docker):
```bash
go test -v -run TestKernelMount
```

Skip kernel tests:
```bash
SKIP_KERNEL_TESTS=1 go test ./...
```

See [test/kernel/README.md](test/kernel/README.md) for more details on kernel testing.
