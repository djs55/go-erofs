package builder

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"sort"

	"github.com/erofs/go-erofs/internal/disk"
	"github.com/erofs/go-erofs/tarfs"
)

// Builder constructs an EROFS filesystem from a tarfs.Tree
type Builder struct {
	tree      *tarfs.Tree
	blockBits uint8
	blockSize uint32

	inodeMap      map[string]uint64
	inodeList     []InodeData
	dataBlocks    []DataBlock
	nextNid       uint64
	nextDataBlock uint32
}

// InodeData holds all information needed to serialize an inode
type InodeData struct {
	Nid       uint64
	Path      string
	Node      *tarfs.Node
	Layout    uint8
	BlockAddr uint32 // data location in blocks
	Children  []DirEntry
	XattrData []byte
}

// DirEntry represents a directory entry to be serialized
type DirEntry struct {
	Name     string
	Nid      uint64
	FileType uint8
}

// DataBlock represents a block of data to be written
type DataBlock struct {
	Addr uint32
	Data []byte
}

// New creates a new builder with the given configuration
func New(tree *tarfs.Tree, blockBits uint8) *Builder {
	if blockBits == 0 {
		blockBits = 12 // default 4KB blocks
	}
	return &Builder{
		tree:          tree,
		blockBits:     blockBits,
		blockSize:     1 << blockBits,
		inodeMap:      make(map[string]uint64),
		nextNid:       16, // root NID is 16
		nextDataBlock: 8,  // first 8 blocks reserved
	}
}

// Build processes the tree and prepares all data structures
func (b *Builder) Build() error {
	// Assign NIDs and process tree
	if err := b.assignNids(b.tree.Root); err != nil {
		return err
	}

	// Calculate metadata size and set data block start
	// Metadata needs space for highest NID + 1 inodes, each is 32 bytes minimum
	maxNid := uint64(0)
	for _, inode := range b.inodeList {
		if inode.Nid > maxNid {
			maxNid = inode.Nid
		}
	}

	// Metadata size in bytes = (maxNid + 1) * 32
	// But we also need space for xattrs, so be conservative and double it
	metadataBytes := (maxNid + 1) * disk.SizeInodeCompact * 2

	// Calculate how many blocks metadata needs
	metadataBlocks := (metadataBytes + uint64(b.blockSize) - 1) / uint64(b.blockSize)

	// Data blocks start after reserved area (8 blocks) + metadata
	b.nextDataBlock = uint32(8 + metadataBlocks)

	// Process each inode
	for i := range b.inodeList {
		if err := b.processInode(&b.inodeList[i]); err != nil {
			return err
		}
	}

	return nil
}

// assignNids performs depth-first traversal to assign NIDs
func (b *Builder) assignNids(node *tarfs.Node) error {
	// Assign NID to this node
	nid := b.nextNid
	b.nextNid++

	b.inodeMap[node.Path] = nid

	inodeData := InodeData{
		Nid:  nid,
		Path: node.Path,
		Node: node,
	}

	// Process children for directories
	if node.Mode.IsDir() {
		// Sort children by name for deterministic output
		names := make([]string, 0, len(node.Children))
		for name := range node.Children {
			names = append(names, name)
		}
		sort.Strings(names)

		// Recursively assign NIDs to children first
		for _, name := range names {
			child := node.Children[name]
			if err := b.assignNids(child); err != nil {
				return err
			}
		}

		// Now create directory entries with assigned NIDs
		for _, name := range names {
			child := node.Children[name]
			childNid := b.inodeMap[child.Path]

			inodeData.Children = append(inodeData.Children, DirEntry{
				Name:     name,
				Nid:      childNid,
				FileType: fileModeToEroFSType(child.Mode),
			})
		}
	}

	b.inodeList = append(b.inodeList, inodeData)
	return nil
}

// processInode prepares an inode for serialization
func (b *Builder) processInode(inode *InodeData) error {
	node := inode.Node

	// Process xattrs
	if len(node.Xattrs) > 0 {
		xattrData, err := b.serializeXattrs(node.Xattrs)
		if err != nil {
			return fmt.Errorf("serializing xattrs for %s: %w", inode.Path, err)
		}
		inode.XattrData = xattrData
	}

	// Allocate data blocks based on file type
	if node.Mode.IsRegular() {
		// Use FLAT_PLAIN layout for MVP
		inode.Layout = disk.LayoutFlatPlain

		if node.Size > 0 {
			// Allocate blocks for file data
			data := make([]byte, node.Size)
			if _, err := io.ReadFull(node.NewReader(), data); err != nil {
				return fmt.Errorf("reading file data for %s: %w", inode.Path, err)
			}

			inode.BlockAddr = b.nextDataBlock
			b.dataBlocks = append(b.dataBlocks, DataBlock{
				Addr: b.nextDataBlock,
				Data: data,
			})

			// Calculate blocks needed (round up)
			blocksNeeded := (uint32(len(data)) + b.blockSize - 1) / b.blockSize
			b.nextDataBlock += blocksNeeded
		}
	} else if node.Mode.IsDir() {
		// Use FLAT_PLAIN layout for directories
		inode.Layout = disk.LayoutFlatPlain

		// Pack directory entries
		dirData, err := b.packDirEntries(inode.Children)
		if err != nil {
			return fmt.Errorf("packing directory entries for %s: %w", inode.Path, err)
		}

		if len(dirData) > 0 {
			// Set directory size to the size of the directory data
			node.Size = int64(len(dirData))

			inode.BlockAddr = b.nextDataBlock
			b.dataBlocks = append(b.dataBlocks, DataBlock{
				Addr: b.nextDataBlock,
				Data: dirData,
			})

			blocksNeeded := (uint32(len(dirData)) + b.blockSize - 1) / b.blockSize
			b.nextDataBlock += blocksNeeded
		}
	} else if node.Mode&fs.ModeSymlink != 0 {
		// Store symlink target in data area
		inode.Layout = disk.LayoutFlatPlain

		linkData := []byte(node.Linkname)
		inode.BlockAddr = b.nextDataBlock
		b.dataBlocks = append(b.dataBlocks, DataBlock{
			Addr: b.nextDataBlock,
			Data: linkData,
		})

		blocksNeeded := (uint32(len(linkData)) + b.blockSize - 1) / b.blockSize
		b.nextDataBlock += blocksNeeded
	}

	return nil
}

// packDirEntries serializes directory entries
func (b *Builder) packDirEntries(entries []DirEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	buf := new(bytes.Buffer)

	// Calculate name offset for each entry
	// Each dirent is 12 bytes, followed by all names
	nameOff := uint16(len(entries) * disk.SizeDirent)
	nameData := new(bytes.Buffer)

	for _, entry := range entries {
		dirent := disk.Dirent{
			Nid:      entry.Nid,
			NameOff:  nameOff,
			FileType: entry.FileType,
			Reserved: 0,
		}

		if err := binary.Write(buf, binary.LittleEndian, &dirent); err != nil {
			return nil, err
		}

		nameData.WriteString(entry.Name)
		nameOff += uint16(len(entry.Name))
	}

	// Append all names after dirents
	buf.Write(nameData.Bytes())

	return buf.Bytes(), nil
}

// serializeXattrs converts xattr map to EROFS xattr format
func (b *Builder) serializeXattrs(xattrs map[string]string) ([]byte, error) {
	if len(xattrs) == 0 {
		return nil, nil
	}

	buf := new(bytes.Buffer)

	// Write xattr header
	header := disk.XattrHeader{
		NameFilter:  0,
		SharedCount: 0,
	}
	if err := binary.Write(buf, binary.LittleEndian, &header); err != nil {
		return nil, err
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(xattrs))
	for k := range xattrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Write xattr entries
	for _, key := range keys {
		value := xattrs[key]

		// Determine prefix index
		prefixIdx, name := splitXattrName(key)

		entry := disk.XattrEntry{
			NameLen:   uint8(len(name)),
			NameIndex: prefixIdx,
			ValueLen:  uint16(len(value)),
		}

		if err := binary.Write(buf, binary.LittleEndian, &entry); err != nil {
			return nil, err
		}

		// Write name and value
		buf.WriteString(name)
		buf.WriteString(value)
	}

	// Align to 4 bytes
	for buf.Len()%4 != 0 {
		buf.WriteByte(0)
	}

	return buf.Bytes(), nil
}

// splitXattrName splits an xattr name into prefix index and name
func splitXattrName(fullName string) (uint8, string) {
	// Standard prefixes
	prefixes := []string{
		"",                         // 0: no prefix
		"user.",                    // 1: user namespace
		"system.posix_acl_access",  // 2
		"system.posix_acl_default", // 3
		"trusted.",                 // 4: trusted namespace
		"",                         // 5: reserved
		"security.",                // 6: security namespace
		"system.",                  // 7: system namespace
	}

	for idx, prefix := range prefixes {
		if len(prefix) > 0 && len(fullName) > len(prefix) && fullName[:len(prefix)] == prefix {
			return uint8(idx), fullName[len(prefix):]
		}
	}

	// No matching prefix, use index 0 with full name
	return 0, fullName
}

// fileModeToEroFSType converts fs.FileMode to EROFS file type
func fileModeToEroFSType(mode fs.FileMode) uint8 {
	switch mode & fs.ModeType {
	case 0:
		return disk.FileTypeReg
	case fs.ModeDir:
		return disk.FileTypeDir
	case fs.ModeSymlink:
		return disk.FileTypeSymlink
	case fs.ModeCharDevice:
		return disk.FileTypeChrdev
	case fs.ModeDevice:
		return disk.FileTypeBlkdev
	case fs.ModeNamedPipe:
		return disk.FileTypeFifo
	case fs.ModeSocket:
		return disk.FileTypeSock
	default:
		return disk.FileTypeReg
	}
}

// GetInodeList returns the list of inodes
func (b *Builder) GetInodeList() []InodeData {
	return b.inodeList
}

// GetDataBlocks returns the list of data blocks
func (b *Builder) GetDataBlocks() []DataBlock {
	return b.dataBlocks
}

// GetBlockBits returns the block size in bits
func (b *Builder) GetBlockBits() uint8 {
	return b.blockBits
}

// GetTotalBlocks returns the total number of blocks used
func (b *Builder) GetTotalBlocks() uint32 {
	return b.nextDataBlock
}

// GetTotalInodes returns the total number of inodes
func (b *Builder) GetTotalInodes() uint64 {
	return uint64(len(b.inodeList))
}
