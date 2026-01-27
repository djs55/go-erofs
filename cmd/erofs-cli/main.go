package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"

	"github.com/erofs/go-erofs"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "stat":
		statCmd()
	case "mkfs":
		mkfsCmd()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: erofs-cli <command> [options]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  stat    Read and display EROFS image contents\n")
	fmt.Fprintf(os.Stderr, "  mkfs    Create EROFS image from tar archive\n")
}

func statCmd() {
	flagSet := flag.NewFlagSet("stat", flag.ExitOnError)
	imgPath := flagSet.String("img", "", "Path to erofs image")
	flagSet.Parse(os.Args[2:])

	if *imgPath == "" {
		fmt.Fprintf(os.Stderr, "Error: -img is required\n")
		flagSet.Usage()
		os.Exit(1)
	}

	f, err := os.Open(*imgPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	img, err := erofs.EroFS(f)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Found valid image...\n")

	err = fs.WalkDir(img, "/", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error visting %s: %w", path, err)
		}
		fmt.Printf("visited: %q\n", path)
		fmt.Printf("\tName: %q\n", entry.Name())
		fmt.Printf("\tType: %o\n", entry.Type())
		if entry.IsDir() {
			fmt.Printf("\tIs a directory: yes\n")
		} else {
			fmt.Printf("\tIs a directory: no\n")
		}
		fi, err := entry.Info()
		if err != nil {
			return fmt.Errorf("error getting info for %s: %w", path, err)
		}
		fmt.Printf("\tMode: %o\n", fi.Mode())
		fmt.Printf("\tModTime: %s\n", fi.ModTime())
		st := fi.Sys().(*erofs.Stat)
		if len(st.Xattrs) > 0 {
			fmt.Printf("\tXattrs:\n")
			for k, v := range st.Xattrs {
				fmt.Printf("\t\t%s: %q\n", k, v)
			}
		}
		if entry.Name() == "." || entry.Name() == ".." {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}

func mkfsCmd() {
	flagSet := flag.NewFlagSet("mkfs", flag.ExitOnError)
	tarPath := flagSet.String("tar", "", "Path to tar archive (use - for stdin)")
	output := flagSet.String("o", "", "Output EROFS image path")
	blockBits := flagSet.Uint("block-bits", 12, "Block size in bits (default 12 = 4KB)")
	forceUID := flagSet.Uint("force-uid", 0, "Override all UIDs (0 = don't override)")
	forceGID := flagSet.Uint("force-gid", 0, "Override all GIDs (0 = don't override)")
	flagSet.Parse(os.Args[2:])

	if *tarPath == "" {
		fmt.Fprintf(os.Stderr, "Error: -tar is required\n")
		flagSet.Usage()
		os.Exit(1)
	}
	if *output == "" {
		fmt.Fprintf(os.Stderr, "Error: -o is required\n")
		flagSet.Usage()
		os.Exit(1)
	}

	// Open tar input
	var tarReader io.Reader
	if *tarPath == "-" {
		tarReader = os.Stdin
	} else {
		f, err := os.Open(*tarPath)
		if err != nil {
			log.Fatalf("Failed to open tar: %v", err)
		}
		defer f.Close()
		tarReader = f
	}

	// Open output file
	outFile, err := os.Create(*output)
	if err != nil {
		log.Fatalf("Failed to create output: %v", err)
	}
	defer outFile.Close()

	// Build options
	var opts []erofs.Option
	opts = append(opts, erofs.WithBlockSize(uint8(*blockBits)))
	if *forceUID > 0 {
		uid := uint32(*forceUID)
		opts = append(opts, erofs.WithForceUID(uid))
	}
	if *forceGID > 0 {
		gid := uint32(*forceGID)
		opts = append(opts, erofs.WithForceGID(gid))
	}

	// Create EROFS image
	fmt.Printf("Creating EROFS image from tar...\n")
	if err := erofs.MakeFromTar(tarReader, outFile, opts...); err != nil {
		log.Fatalf("Failed to create EROFS: %v", err)
	}

	fmt.Printf("Successfully created %s\n", *output)
}
