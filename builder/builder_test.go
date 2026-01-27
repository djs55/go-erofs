package builder

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"testing"
	"time"

	"github.com/erofs/go-erofs/internal/disk"
	"github.com/erofs/go-erofs/tarfs"
)

func TestNew(t *testing.T) {
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path: "",
			Mode: fs.ModeDir | 0755,
		},
	}

	b := New(tree, 12)
	if b.blockBits != 12 {
		t.Errorf("expected blockBits=12, got %d", b.blockBits)
	}
	if b.blockSize != 4096 {
		t.Errorf("expected blockSize=4096, got %d", b.blockSize)
	}
	if b.nextNid != 16 {
		t.Errorf("expected nextNid=16, got %d", b.nextNid)
	}
}

func TestBuild_SimpleFile(t *testing.T) {
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path:     "",
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*tarfs.Node),
		},
		Nodes: make(map[string]*tarfs.Node),
	}

	fileData := []byte("hello world")
	file := &tarfs.Node{
		Path:    "test.txt",
		Mode:    0644,
		Size:    int64(len(fileData)),
		RawData: fileData,
	}
	tree.Root.Children["test.txt"] = file
	tree.Nodes["test.txt"] = file
	tree.Nodes[""] = tree.Root

	b := New(tree, 12)
	if err := b.Build(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Check that we have 2 inodes (root + file)
	if len(b.inodeList) != 2 {
		t.Errorf("expected 2 inodes, got %d", len(b.inodeList))
	}

	// Find root inode by path
	var rootInode *InodeData
	var fileInode *InodeData
	for i := range b.inodeList {
		if b.inodeList[i].Path == "" {
			rootInode = &b.inodeList[i]
		} else if b.inodeList[i].Path == "test.txt" {
			fileInode = &b.inodeList[i]
		}
	}

	if rootInode == nil {
		t.Fatal("root inode not found")
	}
	if rootInode.Nid != 16 {
		t.Errorf("expected root nid=16, got %d", rootInode.Nid)
	}
	if !rootInode.Node.Mode.IsDir() {
		t.Error("root should be a directory")
	}
	if len(rootInode.Children) != 1 {
		t.Errorf("expected 1 child, got %d", len(rootInode.Children))
	}

	if fileInode == nil {
		t.Fatal("file inode not found")
	}
	if fileInode.Layout != disk.LayoutFlatPlain {
		t.Errorf("expected LayoutFlatPlain, got %d", fileInode.Layout)
	}
	if fileInode.Node.Size != int64(len(fileData)) {
		t.Errorf("expected size %d, got %d", len(fileData), fileInode.Node.Size)
	}

	// Check that data was allocated
	if len(b.dataBlocks) != 2 {
		t.Errorf("expected 2 data blocks (dir + file), got %d", len(b.dataBlocks))
	}
}

func TestBuild_Directory(t *testing.T) {
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path:     "",
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*tarfs.Node),
		},
		Nodes: make(map[string]*tarfs.Node),
	}

	dir := &tarfs.Node{
		Path:     "subdir",
		Mode:     fs.ModeDir | 0755,
		Children: make(map[string]*tarfs.Node),
	}
	tree.Root.Children["subdir"] = dir
	tree.Nodes["subdir"] = dir
	tree.Nodes[""] = tree.Root

	b := New(tree, 12)
	if err := b.Build(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Should have root + subdir
	if len(b.inodeList) != 2 {
		t.Errorf("expected 2 inodes, got %d", len(b.inodeList))
	}

	// Find root inode by path
	var rootInode *InodeData
	for i := range b.inodeList {
		if b.inodeList[i].Path == "" {
			rootInode = &b.inodeList[i]
			break
		}
	}

	if rootInode == nil {
		t.Fatal("root inode not found")
	}

	// Check that subdir appears in root's children
	if len(rootInode.Children) != 1 {
		t.Fatalf("expected 1 child in root, got %d", len(rootInode.Children))
	}
	if rootInode.Children[0].Name != "subdir" {
		t.Errorf("expected child name 'subdir', got %q", rootInode.Children[0].Name)
	}
	if rootInode.Children[0].FileType != disk.FileTypeDir {
		t.Errorf("expected FileTypeDir, got %d", rootInode.Children[0].FileType)
	}
}

func TestBuild_Symlink(t *testing.T) {
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path:     "",
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*tarfs.Node),
		},
		Nodes: make(map[string]*tarfs.Node),
	}

	link := &tarfs.Node{
		Path:     "link",
		Mode:     fs.ModeSymlink | 0777,
		Linkname: "target",
		Size:     6,
	}
	tree.Root.Children["link"] = link
	tree.Nodes["link"] = link
	tree.Nodes[""] = tree.Root

	b := New(tree, 12)
	if err := b.Build(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Find the symlink inode
	var linkInode *InodeData
	for i := range b.inodeList {
		if b.inodeList[i].Path == "link" {
			linkInode = &b.inodeList[i]
			break
		}
	}

	if linkInode == nil {
		t.Fatal("link inode not found")
	}

	if linkInode.Layout != disk.LayoutFlatPlain {
		t.Errorf("expected LayoutFlatPlain, got %d", linkInode.Layout)
	}

	// Check that data was allocated for the symlink target
	found := false
	for _, block := range b.dataBlocks {
		if block.Addr == linkInode.BlockAddr {
			if string(block.Data) != "target" {
				t.Errorf("expected symlink data 'target', got %q", string(block.Data))
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("symlink data block not found")
	}
}

func TestBuild_Xattrs(t *testing.T) {
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path:     "",
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*tarfs.Node),
		},
		Nodes: make(map[string]*tarfs.Node),
	}

	file := &tarfs.Node{
		Path:    "file.txt",
		Mode:    0644,
		Size:    4,
		RawData: []byte("test"),
		Xattrs: map[string]string{
			"user.key1":      "value1",
			"security.label": "system_u:object_r:tmp_t:s0",
		},
	}
	tree.Root.Children["file.txt"] = file
	tree.Nodes["file.txt"] = file
	tree.Nodes[""] = tree.Root

	b := New(tree, 12)
	if err := b.Build(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Find the file inode
	var fileInode *InodeData
	for i := range b.inodeList {
		if b.inodeList[i].Path == "file.txt" {
			fileInode = &b.inodeList[i]
			break
		}
	}

	if fileInode == nil {
		t.Fatal("file inode not found")
	}

	if len(fileInode.XattrData) == 0 {
		t.Error("expected xattr data, got none")
	}

	// Verify xattr data has proper structure (header + entries)
	if len(fileInode.XattrData) < disk.SizeXattrBodyHeader {
		t.Errorf("xattr data too small: %d bytes", len(fileInode.XattrData))
	}
}

func TestPackDirEntries(t *testing.T) {
	tree := &tarfs.Tree{Root: &tarfs.Node{Path: "", Mode: fs.ModeDir}}
	b := New(tree, 12)

	entries := []DirEntry{
		{Name: "file1.txt", Nid: 17, FileType: disk.FileTypeReg},
		{Name: "file2.txt", Nid: 18, FileType: disk.FileTypeReg},
		{Name: "dir", Nid: 19, FileType: disk.FileTypeDir},
	}

	data, err := b.packDirEntries(entries)
	if err != nil {
		t.Fatalf("packDirEntries failed: %v", err)
	}

	// Should have 3 dirents (12 bytes each) + names
	expectedMinSize := 3 * disk.SizeDirent
	if len(data) < expectedMinSize {
		t.Errorf("expected at least %d bytes, got %d", expectedMinSize, len(data))
	}

	// Parse first dirent
	buf := bytes.NewReader(data)
	var dirent disk.Dirent
	if err := binary.Read(buf, binary.LittleEndian, &dirent); err != nil {
		t.Fatalf("reading dirent: %v", err)
	}

	if dirent.Nid != 17 {
		t.Errorf("expected nid=17, got %d", dirent.Nid)
	}
	if dirent.FileType != disk.FileTypeReg {
		t.Errorf("expected FileTypeReg, got %d", dirent.FileType)
	}
}

func TestSerializeXattrs(t *testing.T) {
	tree := &tarfs.Tree{Root: &tarfs.Node{Path: "", Mode: fs.ModeDir}}
	b := New(tree, 12)

	xattrs := map[string]string{
		"user.key1": "value1",
		"user.key2": "value2",
	}

	data, err := b.serializeXattrs(xattrs)
	if err != nil {
		t.Fatalf("serializeXattrs failed: %v", err)
	}

	// Should have header + entries + data
	if len(data) < disk.SizeXattrBodyHeader {
		t.Errorf("xattr data too small: %d bytes", len(data))
	}

	// Should be 4-byte aligned
	if len(data)%4 != 0 {
		t.Errorf("xattr data not 4-byte aligned: %d bytes", len(data))
	}
}

func TestSplitXattrName(t *testing.T) {
	tests := []struct {
		fullName string
		wantIdx  uint8
		wantName string
	}{
		{"user.key", 1, "key"},
		{"security.label", 6, "label"},
		{"trusted.foo", 4, "foo"},
		{"system.bar", 7, "bar"},
		{"unknown.prefix", 0, "unknown.prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.fullName, func(t *testing.T) {
			idx, name := splitXattrName(tt.fullName)
			if idx != tt.wantIdx {
				t.Errorf("expected idx=%d, got %d", tt.wantIdx, idx)
			}
			if name != tt.wantName {
				t.Errorf("expected name=%q, got %q", tt.wantName, name)
			}
		})
	}
}

func TestFileModeToEroFSType(t *testing.T) {
	tests := []struct {
		mode fs.FileMode
		want uint8
	}{
		{0644, disk.FileTypeReg},
		{fs.ModeDir | 0755, disk.FileTypeDir},
		{fs.ModeSymlink | 0777, disk.FileTypeSymlink},
		{fs.ModeCharDevice | 0600, disk.FileTypeChrdev},
		{fs.ModeDevice | 0600, disk.FileTypeBlkdev},
		{fs.ModeNamedPipe | 0600, disk.FileTypeFifo},
		{fs.ModeSocket | 0600, disk.FileTypeSock},
	}

	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			got := fileModeToEroFSType(tt.mode)
			if got != tt.want {
				t.Errorf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestBuild_NidOrdering(t *testing.T) {
	// Create a tree with multiple levels to test NID assignment order
	tree := &tarfs.Tree{
		Root: &tarfs.Node{
			Path:     "",
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*tarfs.Node),
		},
		Nodes: make(map[string]*tarfs.Node),
	}

	dir1 := &tarfs.Node{
		Path:     "dir1",
		Mode:     fs.ModeDir | 0755,
		Children: make(map[string]*tarfs.Node),
	}
	file1 := &tarfs.Node{
		Path:    "dir1/file1.txt",
		Mode:    0644,
		Size:    4,
		RawData: []byte("test"),
	}
	dir1.Children["file1.txt"] = file1

	file2 := &tarfs.Node{
		Path:    "file2.txt",
		Mode:    0644,
		Size:    4,
		RawData: []byte("test"),
	}

	tree.Root.Children["dir1"] = dir1
	tree.Root.Children["file2.txt"] = file2
	tree.Nodes[""] = tree.Root
	tree.Nodes["dir1"] = dir1
	tree.Nodes["dir1/file1.txt"] = file1
	tree.Nodes["file2.txt"] = file2

	b := New(tree, 12)
	if err := b.Build(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Root should be NID 16
	if b.inodeMap[""] != 16 {
		t.Errorf("expected root nid=16, got %d", b.inodeMap[""])
	}

	// All NIDs should be unique
	nids := make(map[uint64]bool)
	for _, nid := range b.inodeMap {
		if nids[nid] {
			t.Errorf("duplicate nid: %d", nid)
		}
		nids[nid] = true
	}

	// Should have 4 unique NIDs
	if len(nids) != 4 {
		t.Errorf("expected 4 unique nids, got %d", len(nids))
	}
}

func createTestNode(path string, mode fs.FileMode, size int64) *tarfs.Node {
	node := &tarfs.Node{
		Path:  path,
		Mode:  mode,
		Size:  size,
		Mtime: time.Now(),
	}
	if mode.IsDir() {
		node.Children = make(map[string]*tarfs.Node)
	} else if mode.IsRegular() && size > 0 {
		node.RawData = make([]byte, size)
	}
	return node
}
