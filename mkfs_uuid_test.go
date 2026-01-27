package erofs

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"
	"time"
)

func TestMakeFromTar_WithUUID(t *testing.T) {
	// Create a simple tar
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	hdr := &tar.Header{
		Name:    "file.txt",
		Mode:    0644,
		Size:    4,
		ModTime: time.Now(),
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("test"))
	tw.Close()

	// UUID to test with
	testUUID := [16]byte{
		0x55, 0x0e, 0x84, 0x00,
		0xe2, 0x9b, 0x41, 0xd4,
		0xa7, 0x16, 0x44, 0x66,
		0x55, 0x44, 0x00, 0x00,
	}

	// Create EROFS with UUID
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(
		bytes.NewReader(tarBuf.Bytes()),
		erofsBuf,
		WithUUID(testUUID),
	); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read the superblock to verify UUID was written
	data := erofsBuf.Bytes()
	if len(data) < 1024+128 {
		t.Fatal("EROFS image too small")
	}

	// Superblock starts at offset 1024
	superblock := data[1024 : 1024+128]

	// UUID is at offset 48 in the superblock (32 bytes after MetaBlkAddr)
	var readUUID [16]byte
	copy(readUUID[:], superblock[48:64])

	// Verify UUID matches
	if readUUID != testUUID {
		t.Errorf("UUID mismatch:\n  expected: %x\n  got:      %x", testUUID, readUUID)
	}

	// Verify the filesystem is still readable
	efs, err := EroFS(bytes.NewReader(erofsBuf.Bytes()))
	if err != nil {
		t.Fatalf("Failed to read EROFS with UUID: %v", err)
	}

	// Verify we can read the file
	file, err := efs.Open("/file.txt")
	if err != nil {
		t.Fatalf("Failed to open file: %v", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(content) != "test" {
		t.Errorf("Expected 'test', got %q", string(content))
	}
}

func TestMakeFromTar_WithoutUUID(t *testing.T) {
	// Create a simple tar
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	hdr := &tar.Header{
		Name:    "file.txt",
		Mode:    0644,
		Size:    4,
		ModTime: time.Now(),
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("test"))
	tw.Close()

	// Create EROFS without UUID
	erofsBuf := new(bytes.Buffer)
	if err := MakeFromTar(bytes.NewReader(tarBuf.Bytes()), erofsBuf); err != nil {
		t.Fatalf("MakeFromTar failed: %v", err)
	}

	// Read the superblock to verify UUID is zero
	data := erofsBuf.Bytes()
	if len(data) < 1024+128 {
		t.Fatal("EROFS image too small")
	}

	// Superblock starts at offset 1024
	superblock := data[1024 : 1024+128]

	// UUID is at offset 48 in the superblock
	var readUUID [16]byte
	copy(readUUID[:], superblock[48:64])

	// Verify UUID is all zeros (default)
	zeroUUID := [16]byte{}
	if readUUID != zeroUUID {
		t.Errorf("Expected zero UUID, got: %x", readUUID)
	}
}
