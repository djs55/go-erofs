package erofs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io/fs"
	"testing"
	"time"
)

func TestMakeFromTar_LargeDirectory(t *testing.T) {
	// Create tar with a directory containing many files
	// This will span multiple 4KB blocks
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Explicitly create the directory
	dirHdr := &tar.Header{
		Name:     "bigdir/",
		Mode:     0755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now(),
	}
	if err := tw.WriteHeader(dirHdr); err != nil {
		t.Fatal(err)
	}

	// Create directory with 500 files
	// Each entry is 12 bytes dirent + name length
	// 500 files with average 10-char names = 500*12 + 500*10 = 11000 bytes
	// That's 3 blocks, which will expose the bug
	numFiles := 500
	fileNames := make([]string, numFiles)

	for i := 0; i < numFiles; i++ {
		name := fmt.Sprintf("bigdir/file-%04d.txt", i)
		fileNames[i] = fmt.Sprintf("file-%04d.txt", i)

		content := []byte(fmt.Sprintf("Content of file %d\n", i))
		hdr := &tar.Header{
			Name:    name,
			Mode:    0644,
			Size:    int64(len(content)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back and verify all files are present
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	// Debug: list root directory
	rootEntries, err := fs.ReadDir(efs, "/")
	if err != nil {
		t.Fatalf("Failed to read root directory: %v", err)
	}
	t.Logf("Root directory contains:")
	for _, e := range rootEntries {
		t.Logf("  - %s", e.Name())
	}

	// Check directory size
	dirFile, err := efs.Open("/bigdir")
	if err != nil {
		t.Fatalf("Failed to open /bigdir: %v", err)
	}
	dirInfo, err := dirFile.Stat()
	if err != nil {
		t.Fatalf("Failed to stat /bigdir: %v", err)
	}
	dirSize := dirInfo.Size()
	blocksNeeded := (dirSize + 4095) / 4096
	t.Logf("Directory size: %d bytes (%d blocks)", dirSize, blocksNeeded)

	if blocksNeeded < 2 {
		t.Fatalf("Expected directory to span at least 2 blocks, got %d", blocksNeeded)
	}

	// Read all entries
	entries, err := fs.ReadDir(efs, "/bigdir")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	if len(entries) != numFiles {
		t.Errorf("Expected %d entries, got %d", numFiles, len(entries))

		// Show which files are missing if count is wrong
		if len(entries) < numFiles {
			foundNames := make(map[string]bool)
			for _, e := range entries {
				foundNames[e.Name()] = true
			}

			missing := 0
			for _, expected := range fileNames {
				if !foundNames[expected] {
					if missing < 10 {
						t.Logf("Missing: %s", expected)
					}
					missing++
				}
			}
			if missing > 10 {
				t.Logf("... and %d more missing files", missing-10)
			}
		}
	}

	// Verify each file exists and has correct content
	for i := 0; i < numFiles; i++ {
		path := fmt.Sprintf("/bigdir/file-%04d.txt", i)
		f, err := efs.Open(path)
		if err != nil {
			// Only show first 10 errors to avoid spam
			if i < 10 {
				t.Errorf("Failed to open %s: %v", path, err)
			}
			continue
		}

		content := make([]byte, 100)
		n, _ := f.Read(content)
		expected := fmt.Sprintf("Content of file %d\n", i)
		if string(content[:n]) != expected {
			if i < 10 {
				t.Errorf("%s: got %q, expected %q", path, content[:n], expected)
			}
		}
		f.Close()
	}

	// Verify we can iterate through the directory in chunks
	dirFile2, _ := efs.Open("/bigdir")
	dir2 := dirFile2.(fs.ReadDirFile)

	totalRead := 0
	for {
		chunk, err := dir2.ReadDir(100)
		if len(chunk) == 0 {
			break
		}
		totalRead += len(chunk)
		if err != nil {
			break
		}
	}

	if totalRead != numFiles {
		t.Errorf("Chunked ReadDir: expected %d total entries, got %d", numFiles, totalRead)
	}
}
