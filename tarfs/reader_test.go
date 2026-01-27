package tarfs

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

func TestFromTar_BasicFile(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "test.txt", Mode: 0644, Size: 11, Data: []byte("hello world")},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	node := tree.Nodes["test.txt"]
	if node == nil {
		t.Fatal("test.txt not found in tree")
	}
	if node.Mode&0777 != 0644 {
		t.Errorf("expected mode 0644, got %o", node.Mode&0777)
	}
	if node.Size != 11 {
		t.Errorf("expected size 11, got %d", node.Size)
	}

	data, err := io.ReadAll(node.Data)
	if err != nil {
		t.Fatalf("reading data: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
}

func TestFromTar_Directory(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "dir/", Mode: 0755, Typeflag: tar.TypeDir},
		{Name: "dir/file.txt", Mode: 0644, Size: 4, Data: []byte("test")},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	dir := tree.Nodes["dir"]
	if dir == nil {
		t.Fatal("dir not found in tree")
	}
	if !dir.Mode.IsDir() {
		t.Error("dir is not a directory")
	}
	if dir.Children == nil {
		t.Fatal("dir has no children")
	}

	file := dir.Children["file.txt"]
	if file == nil {
		t.Fatal("file.txt not found in dir children")
	}
	if file.Path != "dir/file.txt" {
		t.Errorf("expected path 'dir/file.txt', got %q", file.Path)
	}
}

func TestFromTar_ImplicitDirectory(t *testing.T) {
	// Create file in subdirectory without explicitly creating the directory
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "a/b/c/file.txt", Mode: 0644, Size: 4, Data: []byte("test")},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	// Check all intermediate directories were created
	if tree.Nodes["a"] == nil {
		t.Error("directory 'a' was not created")
	}
	if tree.Nodes["a/b"] == nil {
		t.Error("directory 'a/b' was not created")
	}
	if tree.Nodes["a/b/c"] == nil {
		t.Error("directory 'a/b/c' was not created")
	}
	if tree.Nodes["a/b/c/file.txt"] == nil {
		t.Error("file 'a/b/c/file.txt' was not created")
	}
}

func TestFromTar_Symlink(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "target.txt", Mode: 0644, Size: 4, Data: []byte("test")},
		{Name: "link.txt", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "target.txt"},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	link := tree.Nodes["link.txt"]
	if link == nil {
		t.Fatal("link.txt not found in tree")
	}
	if link.Mode&fs.ModeSymlink == 0 {
		t.Error("link.txt is not a symlink")
	}
	if link.Linkname != "target.txt" {
		t.Errorf("expected linkname 'target.txt', got %q", link.Linkname)
	}
}

func TestFromTar_Hardlink(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "original.txt", Mode: 0644, Size: 11, Data: []byte("hello world")},
		{Name: "hardlink.txt", Mode: 0644, Typeflag: tar.TypeLink, Linkname: "original.txt"},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	original := tree.Nodes["original.txt"]
	if original == nil {
		t.Fatal("original.txt not found in tree")
	}

	hardlink := tree.Nodes["hardlink.txt"]
	if hardlink == nil {
		t.Fatal("hardlink.txt not found in tree")
	}

	if hardlink.Hardlink != original {
		t.Error("hardlink does not point to original")
	}
	if hardlink.Size != original.Size {
		t.Errorf("hardlink size %d != original size %d", hardlink.Size, original.Size)
	}

	// Both should have the same data
	data1, _ := io.ReadAll(original.Data)
	data2, _ := io.ReadAll(hardlink.Data)
	if !bytes.Equal(data1, data2) {
		t.Error("hardlink data does not match original data")
	}
}

func TestFromTar_PAXXattrs(t *testing.T) {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	hdr := &tar.Header{
		Name: "file.txt",
		Mode: 0644,
		Size: 4,
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.key1":      "value1",
			"LIBARCHIVE.xattr.user.key2":  "value2",
			"SCHILY.xattr.security.label": "system_u:object_r:tmp_t:s0",
		},
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	node := tree.Nodes["file.txt"]
	if node == nil {
		t.Fatal("file.txt not found in tree")
	}

	if len(node.Xattrs) != 3 {
		t.Errorf("expected 3 xattrs, got %d", len(node.Xattrs))
	}
	if node.Xattrs["user.key1"] != "value1" {
		t.Errorf("expected user.key1=value1, got %q", node.Xattrs["user.key1"])
	}
	if node.Xattrs["user.key2"] != "value2" {
		t.Errorf("expected user.key2=value2, got %q", node.Xattrs["user.key2"])
	}
	if node.Xattrs["security.label"] != "system_u:object_r:tmp_t:s0" {
		t.Errorf("expected security label, got %q", node.Xattrs["security.label"])
	}
}

func TestFromTar_DeviceNodes(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "chardev", Mode: 0600, Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3},
		{Name: "blockdev", Mode: 0600, Typeflag: tar.TypeBlock, Devmajor: 8, Devminor: 0},
	})

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	chardev := tree.Nodes["chardev"]
	if chardev == nil {
		t.Fatal("chardev not found in tree")
	}
	if chardev.Mode&fs.ModeCharDevice == 0 {
		t.Error("chardev is not a character device")
	}
	if chardev.Devmajor != 1 || chardev.Devminor != 3 {
		t.Errorf("expected dev 1:3, got %d:%d", chardev.Devmajor, chardev.Devminor)
	}

	blockdev := tree.Nodes["blockdev"]
	if blockdev == nil {
		t.Fatal("blockdev not found in tree")
	}
	if blockdev.Mode&fs.ModeDevice == 0 {
		t.Error("blockdev is not a block device")
	}
	if blockdev.Devmajor != 8 || blockdev.Devminor != 0 {
		t.Errorf("expected dev 8:0, got %d:%d", blockdev.Devmajor, blockdev.Devminor)
	}
}

func TestFromTar_InvalidPaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"absolute path", "/etc/passwd"},
		{"parent reference", "../etc/passwd"},
		{"parent in middle", "foo/../../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := createTarWithFiles(t, []tarEntry{
				{Name: tt.path, Mode: 0644, Size: 4, Data: []byte("test")},
			})

			_, err := FromTar(bytes.NewReader(buf.Bytes()))
			if err == nil {
				t.Errorf("expected error for path %q, got nil", tt.path)
			}
		})
	}
}

func TestFromTar_HardlinkToNonExistent(t *testing.T) {
	buf := createTarWithFiles(t, []tarEntry{
		{Name: "hardlink.txt", Mode: 0644, Typeflag: tar.TypeLink, Linkname: "nonexistent.txt"},
	})

	_, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Error("expected error for hardlink to non-existent file")
	}
	if !strings.Contains(err.Error(), "non-existent") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestFromTar_ModTime(t *testing.T) {
	modTime := time.Date(2024, 1, 15, 10, 30, 45, 123456789, time.UTC)
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	hdr := &tar.Header{
		Name:    "file.txt",
		Mode:    0644,
		Size:    4,
		ModTime: modTime,
		Format:  tar.FormatPAX, // PAX format preserves nanoseconds
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	tw.Close()

	tree, err := FromTar(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("FromTar failed: %v", err)
	}

	node := tree.Nodes["file.txt"]
	if node == nil {
		t.Fatal("file.txt not found in tree")
	}

	if !node.Mtime.Equal(modTime) {
		t.Errorf("expected mtime %v, got %v", modTime, node.Mtime)
	}
	if node.MtimeNs != uint32(modTime.Nanosecond()) {
		t.Errorf("expected mtimeNs %d, got %d", modTime.Nanosecond(), node.MtimeNs)
	}
}

// Helper types and functions

type tarEntry struct {
	Name     string
	Mode     int64
	Size     int64
	Data     []byte
	Typeflag byte
	Linkname string
	Devmajor int64
	Devminor int64
}

func createTarWithFiles(t *testing.T, entries []tarEntry) *bytes.Buffer {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	for _, entry := range entries {
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}

		hdr := &tar.Header{
			Name:     entry.Name,
			Mode:     entry.Mode,
			Size:     entry.Size,
			Typeflag: typeflag,
			Linkname: entry.Linkname,
			Devmajor: entry.Devmajor,
			Devminor: entry.Devminor,
			ModTime:  time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(entry.Data) > 0 {
			if _, err := tw.Write(entry.Data); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf
}
