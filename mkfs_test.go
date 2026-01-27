package erofs

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"testing"
	"time"
)

func TestMakeFromTar_SimpleFile(t *testing.T) {
	// Create a simple tar archive
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	files := []struct {
		name string
		mode int64
		data []byte
	}{
		{"hello.txt", 0644, []byte("Hello, World!")},
		{"dir/", 0755, nil},
		{"dir/file.txt", 0644, []byte("nested file")},
	}

	for _, f := range files {
		hdr := &tar.Header{
			Name:    f.name,
			Mode:    f.mode,
			Size:    int64(len(f.data)),
			ModTime: time.Now(),
		}
		if len(f.data) == 0 && hdr.Name[len(hdr.Name)-1] == '/' {
			hdr.Typeflag = tar.TypeDir
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(f.data) > 0 {
			if _, err := tw.Write(f.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back using EroFS reader
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	// Verify root directory
	root, err := efs.Open("/")
	if err != nil {
		t.Fatalf("opening root: %v", err)
	}
	defer root.Close()

	rootInfo, err := root.Stat()
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !rootInfo.IsDir() {
		t.Error("root is not a directory")
	}

	// Verify hello.txt
	helloFile, err := efs.Open("/hello.txt")
	if err != nil {
		t.Fatalf("opening hello.txt: %v", err)
	}
	defer helloFile.Close()

	helloData, err := io.ReadAll(helloFile)
	if err != nil {
		t.Fatalf("reading hello.txt: %v", err)
	}
	if string(helloData) != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got %q", string(helloData))
	}

	// Verify nested file
	nestedFile, err := efs.Open("/dir/file.txt")
	if err != nil {
		t.Fatalf("opening dir/file.txt: %v", err)
	}
	defer nestedFile.Close()

	nestedData, err := io.ReadAll(nestedFile)
	if err != nil {
		t.Fatalf("reading dir/file.txt: %v", err)
	}
	if string(nestedData) != "nested file" {
		t.Errorf("expected 'nested file', got %q", string(nestedData))
	}
}

func TestMakeFromTar_Symlink(t *testing.T) {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Create target file
	targetHdr := &tar.Header{
		Name:    "target.txt",
		Mode:    0644,
		Size:    11,
		ModTime: time.Now(),
	}
	tw.WriteHeader(targetHdr)
	tw.Write([]byte("target data"))

	// Create symlink
	linkHdr := &tar.Header{
		Name:     "link.txt",
		Mode:     0777,
		Typeflag: tar.TypeSymlink,
		Linkname: "target.txt",
		ModTime:  time.Now(),
	}
	tw.WriteHeader(linkHdr)
	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	// Verify symlink
	linkFile, err := efs.Open("/link.txt")
	if err != nil {
		t.Fatalf("opening link.txt: %v", err)
	}
	defer linkFile.Close()

	linkInfo, err := linkFile.Stat()
	if err != nil {
		t.Fatalf("stat link.txt: %v", err)
	}
	if linkInfo.Mode()&fs.ModeSymlink == 0 {
		t.Error("link.txt is not a symlink")
	}
}

func TestMakeFromTar_WithOptions(t *testing.T) {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	hdr := &tar.Header{
		Name:    "file.txt",
		Mode:    0644,
		Size:    4,
		Uid:     1000,
		Gid:     1000,
		ModTime: time.Now(),
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("test"))
	tw.Close()

	// Convert with UID/GID override
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(
		bytes.NewReader(tarBuf.Bytes()),
		erofsBuf,
		WithForceUID(0),
		WithForceGID(0),
	); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	file, err := efs.Open("/file.txt")
	if err != nil {
		t.Fatalf("opening file.txt: %v", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat file.txt: %v", err)
	}

	// Note: The current EroFS reader may not expose UID/GID in FileInfo
	// This test verifies the conversion doesn't fail with options
	_ = info
}

func TestMakeFromTar_EmptyTar(t *testing.T) {
	// Create empty tar (just root directory)
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)
	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	// Should have at least root directory
	root, err := efs.Open("/")
	if err != nil {
		t.Fatalf("opening root: %v", err)
	}
	defer root.Close()

	rootInfo, err := root.Stat()
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !rootInfo.IsDir() {
		t.Error("root is not a directory")
	}
}

func TestMakeFromTar_LargeFile(t *testing.T) {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Create a file larger than one block (4KB default)
	largeData := bytes.Repeat([]byte("x"), 10000)

	hdr := &tar.Header{
		Name:    "large.txt",
		Mode:    0644,
		Size:    int64(len(largeData)),
		ModTime: time.Now(),
	}
	tw.WriteHeader(hdr)
	tw.Write(largeData)
	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	file, err := efs.Open("/large.txt")
	if err != nil {
		t.Fatalf("opening large.txt: %v", err)
	}
	defer file.Close()

	readData, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("reading large.txt: %v", err)
	}

	if len(readData) != len(largeData) {
		t.Errorf("expected %d bytes, got %d", len(largeData), len(readData))
	}
	if !bytes.Equal(readData, largeData) {
		t.Error("large file data doesn't match")
	}
}

func TestMakeFromTar_WithXattrs(t *testing.T) {
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	hdr := &tar.Header{
		Name:   "file.txt",
		Mode:   0644,
		Size:   4,
		Format: tar.FormatPAX,
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.key": "value",
		},
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("test"))
	tw.Close()

	// Convert to EROFS
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read back
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("EroFS failed: %v", err)
	}

	file, err := efs.Open("/file.txt")
	if err != nil {
		t.Fatalf("opening file.txt: %v", err)
	}
	defer file.Close()

	// Verify file can be read (xattr support may vary)
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("reading file.txt: %v", err)
	}
	if string(data) != "test" {
		t.Errorf("expected 'test', got %q", string(data))
	}
}
