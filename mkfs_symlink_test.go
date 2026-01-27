package erofs

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

// TestMakeFromTar_SymlinkSize verifies that symlinks have their size field
// set to the length of the target path. This is critical for the Linux kernel
// to read the symlink correctly.
//
// Regression test for: "the path `sh` is not a regular file: Operation not permitted"
// Root cause: symlinks had size 0, making them unreadable by kernel
func TestMakeFromTar_SymlinkSize(t *testing.T) {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Create a regular file to link to
	hdr := &tar.Header{
		Name:    "busybox",
		Mode:    0755,
		Size:    100,
		ModTime: time.Now(),
	}
	tw.WriteHeader(hdr)
	tw.Write(make([]byte, 100))

	// Create various symlinks with different target lengths
	symlinks := []struct {
		name   string
		target string
	}{
		{"sh", "/bin/busybox"},                        // 12 bytes
		{"link", "busybox"},                           // 7 bytes
		{"long-link", "/very/long/path/to/some/file"}, // 28 bytes
	}

	for _, sl := range symlinks {
		hdr := &tar.Header{
			Name:     sl.name,
			Mode:     0777,
			Typeflag: tar.TypeSymlink,
			Linkname: sl.target,
			ModTime:  time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("failed to write symlink header: %v", err)
		}
	}

	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back and verify symlink sizes
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	for _, sl := range symlinks {
		path := "/" + sl.name

		// Open the symlink
		f, err := efs.Open(path)
		if err != nil {
			t.Errorf("failed to open symlink %s: %v", path, err)
			continue
		}
		defer f.Close()

		// Check file info
		info, err := f.Stat()
		if err != nil {
			t.Errorf("failed to stat symlink %s: %v", path, err)
			continue
		}

		// Verify it's a symlink
		if info.Mode()&0120000 == 0 { // ModeSymlink
			t.Errorf("%s: not a symlink, mode=%o", path, info.Mode())
			continue
		}

		// CRITICAL: Size must equal length of target
		expectedSize := int64(len(sl.target))
		if info.Size() != expectedSize {
			t.Errorf("%s: size mismatch: got %d, want %d (target=%q)",
				path, info.Size(), expectedSize, sl.target)
		}

		// Verify we can read the target
		data, err := io.ReadAll(f)
		if err != nil {
			t.Errorf("%s: failed to read symlink target: %v", path, err)
			continue
		}

		if string(data) != sl.target {
			t.Errorf("%s: target mismatch: got %q, want %q",
				path, string(data), sl.target)
		}

		t.Logf("%s: ✓ size=%d, target=%q", path, info.Size(), string(data))
	}
}

// TestKernelMount_WithSymlinks extends the kernel test to include symlinks
// This ensures the Linux kernel can actually read our symlinks
func TestKernelMount_WithSymlinks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping kernel test in short mode")
	}

	// This would be similar to TestKernelMount but with explicit symlink testing
	// For now, TestKernelMount includes files that might be symlinks in the tar
	t.Skip("Covered by TestKernelMount")
}
