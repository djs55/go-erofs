package erofs

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"testing"
	"time"
)

func TestMakeFromTar_HardlinkNIDSharing(t *testing.T) {
	// Create tar with hardlinks
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Add original file
	busyboxData := bytes.Repeat([]byte("BUSYBOX"), 1000)
	hdr := &tar.Header{
		Name:    "bin/busybox",
		Mode:    0755,
		Size:    int64(len(busyboxData)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(busyboxData); err != nil {
		t.Fatal(err)
	}

	// Add hardlinks (tar.TypeLink, not TypeSymlink)
	hardlinks := []string{"bin/sh", "bin/true", "bin/ls", "bin/cat"}
	for _, name := range hardlinks {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0755,
			Typeflag: tar.TypeLink,
			Linkname: "bin/busybox",
			ModTime:  time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
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

	// Read back and verify
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	// Get NIDs for all files
	nids := make(map[string]int64)
	nlinks := make(map[string]int)
	sizes := make(map[string]int64)

	// Build list of basenames for checking
	baseNames := []string{"busybox", "sh", "true", "ls", "cat"}
	for _, basename := range baseNames {
		path := "/bin/" + basename
		f, err := efs.Open(path)
		if err != nil {
			t.Fatalf("Failed to open %s: %v", path, err)
		}
		info, err := f.Stat()
		if err != nil {
			t.Fatalf("Failed to stat %s: %v", path, err)
		}

		stat, ok := info.Sys().(*Stat)
		if !ok {
			t.Fatalf("%s: Sys() didn't return *Stat", path)
		}

		nids[basename] = stat.Inode
		nlinks[basename] = stat.Nlink
		sizes[basename] = info.Size()
		f.Close()
	}

	// Verify all hardlinks share the same NID as busybox
	busyboxNid := nids["busybox"]
	t.Logf("busybox NID: %d", busyboxNid)

	for _, basename := range []string{"sh", "true", "ls", "cat"} {
		if nids[basename] != busyboxNid {
			t.Errorf("%s has NID %d, expected %d (same as busybox)", basename, nids[basename], busyboxNid)
		} else {
			t.Logf("✓ %s shares NID %d with busybox", basename, busyboxNid)
		}
	}

	// Verify all have the same size
	expectedSize := int64(len(busyboxData))
	for _, basename := range baseNames {
		if sizes[basename] != expectedSize {
			t.Errorf("%s has size %d, expected %d", basename, sizes[basename], expectedSize)
		}
	}

	// Verify nlink count is correct (1 original + 4 hardlinks = 5)
	expectedNlink := len(baseNames)
	for _, basename := range baseNames {
		if nlinks[basename] != expectedNlink {
			t.Errorf("%s has nlink %d, expected %d", basename, nlinks[basename], expectedNlink)
		} else {
			t.Logf("✓ %s has correct nlink count: %d", basename, nlinks[basename])
		}
	}

	// Verify files are regular files, not symlinks
	for _, basename := range baseNames {
		path := "/bin/" + basename
		f, _ := efs.Open(path)
		info, _ := f.Stat()
		if info.Mode()&fs.ModeSymlink != 0 {
			t.Errorf("%s is a symlink, should be regular file", path)
		}
		f.Close()
	}
}
