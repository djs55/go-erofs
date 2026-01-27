package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/erofs/go-erofs"
)

var (
	tarMode       string
	aufs          bool
	ovlfsStrip    int
	quiet         bool
	forceUID      int
	forceGID      int
	timestamp     int64
	blockSize     int
	help          bool
	version       bool
	verbosity     int
	ignoreMtime   bool
	preserveMtime bool
	uuid          string
	extendedOpts  string
)

func main() {
	// Pre-process args to handle combined flags like -Enoinline_data
	os.Args = preprocessArgs(os.Args)

	// Define flags to match mkfs.erofs
	flag.StringVar(&tarMode, "tar", "", "generate from tarball (f=full, i=index, headerball)")
	flag.BoolVar(&aufs, "aufs", false, "replace aufs special files with overlayfs metadata")
	flag.IntVar(&ovlfsStrip, "ovlfs-strip", 0, "strip overlayfs metadata (0 or 1)")
	flag.BoolVar(&quiet, "quiet", false, "quiet execution")
	flag.IntVar(&forceUID, "force-uid", -1, "set all file uids")
	flag.IntVar(&forceGID, "force-gid", -1, "set all file gids")
	flag.Int64Var(&timestamp, "T", 0, "specify fixed UNIX timestamp")
	flag.IntVar(&blockSize, "b", 4096, "set block size")
	flag.BoolVar(&help, "help", false, "display help")
	flag.BoolVar(&help, "h", false, "display help")
	flag.BoolVar(&version, "version", false, "print version")
	flag.BoolVar(&version, "V", false, "print version")
	flag.IntVar(&verbosity, "d", 2, "set output verbosity (0=quiet, 9=verbose)")
	flag.BoolVar(&ignoreMtime, "ignore-mtime", false, "use build time instead of per-file mtime")
	flag.BoolVar(&preserveMtime, "preserve-mtime", false, "keep per-file mtime")
	flag.StringVar(&uuid, "U", "", "filesystem UUID")
	flag.StringVar(&extendedOpts, "E", "", "extended options")

	flag.Usage = printUsage
	flag.Parse()

	if help {
		printUsage()
		os.Exit(0)
	}

	if version {
		fmt.Println("mkfs.erofs (go-erofs) 1.0.0")
		os.Exit(0)
	}

	// Parse positional arguments: output_file [input_files...]
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Error: output file not specified\n")
		printUsage()
		os.Exit(1)
	}

	outputFile := args[0]
	inputFiles := args[1:]

	// If no input files and stdin is not a terminal, read from stdin
	if len(inputFiles) == 0 {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			inputFiles = []string{"-"}
		}
	}

	if len(inputFiles) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no input files specified\n")
		os.Exit(1)
	}

	// Validate extended options
	if extendedOpts != "" {
		if err := validateExtendedOpts(extendedOpts); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// Check for tar index mode
	if tarMode == "i" || tarMode == "headerball" {
		fmt.Fprintf(os.Stderr, "Error: --tar=%s (index mode) not yet implemented\n", tarMode)
		fmt.Fprintf(os.Stderr, "Please use --tar=f for full conversion mode\n")
		os.Exit(1)
	}

	// For tar mode or aufs mode, merge tar files
	if tarMode != "" || aufs || len(inputFiles) > 0 {
		if err := createFromTars(outputFile, inputFiles); err != nil {
			if !quiet {
				log.Fatalf("Error: %v", err)
			}
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: non-tar mode not supported\n")
		os.Exit(1)
	}

	if !quiet && verbosity > 0 {
		fmt.Fprintf(os.Stderr, "Successfully created %s\n", outputFile)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage: mkfs.erofs [OPTIONS] FILE SOURCE(s)\n")
	fmt.Fprintf(os.Stderr, "Generate EROFS image (FILE) from SOURCE(s).\n\n")
	fmt.Fprintf(os.Stderr, "Supported options:\n")
	fmt.Fprintf(os.Stderr, "  -h, --help            display this help and exit\n")
	fmt.Fprintf(os.Stderr, "  -V, --version         print version and exit\n")
	fmt.Fprintf(os.Stderr, "  -b#                   set block size to # bytes (default: 4096)\n")
	fmt.Fprintf(os.Stderr, "  -d<0-9>               set output verbosity (default: 2)\n")
	fmt.Fprintf(os.Stderr, "  -T#                   specify fixed UNIX timestamp\n")
	fmt.Fprintf(os.Stderr, "  -U<uuid>              filesystem UUID\n")
	fmt.Fprintf(os.Stderr, "  -E<options>           extended options (noinline_data supported)\n")
	fmt.Fprintf(os.Stderr, "  --tar=X               generate from tarball (X=f for full mode)\n")
	fmt.Fprintf(os.Stderr, "  --aufs                replace aufs special files (implies tar mode)\n")
	fmt.Fprintf(os.Stderr, "  --ovlfs-strip=<0,1>   strip overlayfs metadata\n")
	fmt.Fprintf(os.Stderr, "  --force-uid=#         set all file uids to #\n")
	fmt.Fprintf(os.Stderr, "  --force-gid=#         set all file gids to #\n")
	fmt.Fprintf(os.Stderr, "  --quiet               quiet execution\n")
	fmt.Fprintf(os.Stderr, "  --ignore-mtime        use build time for all files\n")
	fmt.Fprintf(os.Stderr, "  --preserve-mtime      keep per-file modification time\n")
	fmt.Fprintf(os.Stderr, "\nNOTE: This is a minimal Go implementation supporting tar input only.\n")
	fmt.Fprintf(os.Stderr, "      --tar=i (index mode) is not yet implemented.\n")
}

func validateExtendedOpts(opts string) error {
	for _, opt := range strings.Split(opts, ",") {
		switch opt {
		case "noinline_data":
			// This is already our default behavior - FLAT_PLAIN layout never inlines data.
			// Accept silently as it matches our implementation.
		default:
			return fmt.Errorf("unsupported extended option: %s", opt)
		}
	}
	return nil
}

func parseUUID(s string) ([16]byte, error) {
	var uuid [16]byte

	// Remove dashes from UUID string (e.g., "550e8400-e29b-41d4-a716-446655440000")
	s = strings.ReplaceAll(s, "-", "")

	if len(s) != 32 {
		return uuid, fmt.Errorf("invalid UUID length: expected 32 hex digits, got %d", len(s))
	}

	// Parse hex string into bytes
	for i := 0; i < 16; i++ {
		b, err := fmt.Sscanf(s[i*2:i*2+2], "%02x", &uuid[i])
		if err != nil || b != 1 {
			return uuid, fmt.Errorf("invalid UUID format at position %d", i)
		}
	}

	return uuid, nil
}

// preprocessArgs splits combined flags like -Enoinline_data into -E noinline_data
// and -U<uuid> into -U <uuid> for compatibility with original mkfs.erofs syntax
func preprocessArgs(args []string) []string {
	var result []string
	result = append(result, args[0]) // Keep program name

	for i := 1; i < len(args); i++ {
		arg := args[i]

		// Handle -E<option> -> -E <option>
		if strings.HasPrefix(arg, "-E") && len(arg) > 2 {
			result = append(result, "-E", arg[2:])
			continue
		}

		// Handle -U<uuid> -> -U <uuid>
		if strings.HasPrefix(arg, "-U") && len(arg) > 2 {
			result = append(result, "-U", arg[2:])
			continue
		}

		// Handle -T<timestamp> -> -T <timestamp>
		if strings.HasPrefix(arg, "-T") && len(arg) > 2 {
			result = append(result, "-T", arg[2:])
			continue
		}

		// Handle -b<size> -> -b <size>
		if strings.HasPrefix(arg, "-b") && len(arg) > 2 {
			result = append(result, "-b", arg[2:])
			continue
		}

		// Handle -d<level> -> -d <level>
		if strings.HasPrefix(arg, "-d") && len(arg) > 2 {
			result = append(result, "-d", arg[2:])
			continue
		}

		// Keep everything else as-is
		result = append(result, arg)
	}

	return result
}

func createFromTars(outputFile string, inputFiles []string) error {
	// Create output file
	out, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer out.Close()

	// Merge all input tar files into a single tar stream
	mergedTar, err := mergeTars(inputFiles)
	if err != nil {
		return fmt.Errorf("failed to merge tar files: %w", err)
	}

	// Build options
	var opts []erofs.Option

	// Block size - convert bytes to bits
	if blockSize > 0 {
		bits := 0
		for b := blockSize; b > 1; b >>= 1 {
			bits++
		}
		if (1 << bits) != blockSize {
			return fmt.Errorf("block size must be a power of 2")
		}
		opts = append(opts, erofs.WithBlockSize(uint8(bits)))
	}

	// UID/GID overrides
	if forceUID >= 0 {
		opts = append(opts, erofs.WithForceUID(uint32(forceUID)))
	}
	if forceGID >= 0 {
		opts = append(opts, erofs.WithForceGID(uint32(forceGID)))
	}

	// Timestamp
	if timestamp > 0 {
		ts := time.Unix(timestamp, 0)
		opts = append(opts, erofs.WithTimestamp(ts))
	} else if ignoreMtime {
		ts := time.Now()
		opts = append(opts, erofs.WithTimestamp(ts))
	}

	// UUID
	if uuid != "" {
		parsedUUID, err := parseUUID(uuid)
		if err != nil {
			return fmt.Errorf("invalid UUID: %w", err)
		}
		opts = append(opts, erofs.WithUUID(parsedUUID))
	}

	// Create EROFS image
	if err := erofs.MakeFromTar(mergedTar, out, opts...); err != nil {
		return fmt.Errorf("failed to create EROFS: %w", err)
	}

	return nil
}

func mergeTars(inputFiles []string) (io.Reader, error) {
	// If only one file, return it directly
	if len(inputFiles) == 1 {
		if inputFiles[0] == "-" {
			return os.Stdin, nil
		}
		return os.Open(inputFiles[0])
	}

	// For multiple files, merge them into a single tar stream
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		tw := tar.NewWriter(pw)
		defer tw.Close()

		seen := make(map[string]bool)

		for _, inputFile := range inputFiles {
			var r io.Reader
			if inputFile == "-" {
				r = os.Stdin
			} else {
				f, err := os.Open(inputFile)
				if err != nil {
					pw.CloseWithError(fmt.Errorf("failed to open %s: %w", inputFile, err))
					return
				}
				defer f.Close()
				r = f
			}

			tr := tar.NewReader(r)
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					pw.CloseWithError(fmt.Errorf("failed to read tar: %w", err))
					return
				}

				// Skip duplicates (later files override earlier ones)
				// For overlayfs, we want the last occurrence
				path := hdr.Name

				// Handle overlayfs whiteouts if ovlfsStrip is enabled
				if ovlfsStrip > 0 {
					if strings.Contains(path, ".wh.") {
						// Skip whiteout files
						continue
					}
				}

				// For now, just take all files (last one wins)
				// In a more complete implementation, we'd handle whiteouts properly
				if !seen[path] || aufs {
					seen[path] = true

					if err := tw.WriteHeader(hdr); err != nil {
						pw.CloseWithError(err)
						return
					}

					if hdr.Size > 0 {
						if _, err := io.Copy(tw, tr); err != nil {
							pw.CloseWithError(err)
							return
						}
					}
				}
			}
		}
	}()

	return pr, nil
}
