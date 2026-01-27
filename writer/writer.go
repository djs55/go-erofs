package writer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"time"

	"github.com/erofs/go-erofs/builder"
	"github.com/erofs/go-erofs/internal/disk"
)

// Writer handles serialization of EROFS filesystem
type Writer struct {
	w         io.Writer
	blockBits uint8
	blockSize uint32

	// Track written data for checksum
	buffer *bytes.Buffer
}

// New creates a new writer
func New(w io.Writer, blockBits uint8) *Writer {
	return &Writer{
		w:         w,
		blockBits: blockBits,
		blockSize: 1 << blockBits,
		buffer:    new(bytes.Buffer),
	}
}

// WriteFS writes a complete EROFS filesystem
func (w *Writer) WriteFS(b *builder.Builder, buildTime time.Time, uuid *[16]byte) error {
	// Write to buffer first so we can calculate checksum
	w.buffer.Reset()

	// Write reserved blocks (8 blocks = 32KB at 4KB block size)
	reservedSize := 8 * int(w.blockSize)
	if err := w.writeZeros(reservedSize); err != nil {
		return fmt.Errorf("writing reserved blocks: %w", err)
	}

	// Write metadata area (inodes + xattrs)
	metaStart := w.buffer.Len()
	if err := w.writeMetadata(b); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	// Align to block boundary
	if err := w.alignToBlock(); err != nil {
		return fmt.Errorf("aligning metadata: %w", err)
	}

	// Write data area
	if err := w.writeDataBlocks(b); err != nil {
		return fmt.Errorf("writing data blocks: %w", err)
	}

	// Calculate blocks used
	totalBytes := w.buffer.Len()
	totalBlocks := (uint32(totalBytes) + w.blockSize - 1) / w.blockSize

	// Create superblock
	sb := disk.SuperBlock{
		MagicNumber:  disk.MagicNumber,
		BlkSizeBits:  w.blockBits,
		RootNid:      16,
		Inos:         b.GetTotalInodes(),
		BuildTime:    uint64(buildTime.Unix()),
		BuildTimeNs:  uint32(buildTime.Nanosecond()),
		Blocks:       totalBlocks,
		MetaBlkAddr:  uint32(metaStart) / w.blockSize,
		XattrBlkAddr: 0, // No shared xattrs for MVP
	}

	// Set UUID if provided
	if uuid != nil {
		sb.UUID = *uuid
	}

	// Calculate checksum over entire image
	sb.Checksum = crc32.ChecksumIEEE(w.buffer.Bytes())

	// Write superblock at offset 1024
	sbBuf := new(bytes.Buffer)
	if err := binary.Write(sbBuf, binary.LittleEndian, &sb); err != nil {
		return fmt.Errorf("encoding superblock: %w", err)
	}

	// Insert superblock at correct position
	finalBuf := new(bytes.Buffer)
	finalBuf.Write(w.buffer.Bytes()[:disk.SuperBlockOffset])
	finalBuf.Write(sbBuf.Bytes())

	// Pad to end of reserved area if needed
	currentPos := disk.SuperBlockOffset + disk.SizeSuperBlock
	if currentPos < reservedSize {
		finalBuf.Write(make([]byte, reservedSize-currentPos))
	}

	// Write rest of the image
	finalBuf.Write(w.buffer.Bytes()[reservedSize:])

	// Write final image to output
	if _, err := io.Copy(w.w, finalBuf); err != nil {
		return fmt.Errorf("writing final image: %w", err)
	}

	return nil
}

// writeMetadata writes all inodes at their correct NID-based positions
func (w *Writer) writeMetadata(b *builder.Builder) error {
	inodes := b.GetInodeList()

	// Find the maximum NID to determine metadata size needed
	maxNid := uint64(0)
	for _, inode := range inodes {
		if inode.Nid > maxNid {
			maxNid = inode.Nid
		}
	}

	// Allocate space for all inodes (each compact inode is 32 bytes)
	// We'll use compact inode size as base, extended inodes will overwrite
	metadataSize := int((maxNid + 1) * disk.SizeInodeCompact)
	metadata := make([]byte, metadataSize)

	// Write each inode at its NID-based offset
	for _, inode := range inodes {
		offset := int(inode.Nid * disk.SizeInodeCompact)

		// Serialize inode to a buffer first
		inodeBuf := new(bytes.Buffer)
		if err := w.serializeInode(inodeBuf, &inode); err != nil {
			return fmt.Errorf("serializing inode %s: %w", inode.Path, err)
		}

		// Copy inode data to metadata at correct offset
		copy(metadata[offset:], inodeBuf.Bytes())

		// Write xattrs immediately after inode
		if len(inode.XattrData) > 0 {
			xattrOffset := offset + inodeBuf.Len()
			// Ensure we have space for xattrs
			if xattrOffset+len(inode.XattrData) > len(metadata) {
				// Grow metadata buffer
				newSize := xattrOffset + len(inode.XattrData)
				newMetadata := make([]byte, newSize)
				copy(newMetadata, metadata)
				metadata = newMetadata
			}
			copy(metadata[xattrOffset:], inode.XattrData)
		}
	}

	// Write metadata to buffer
	if _, err := w.buffer.Write(metadata); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}

	return nil
}

// serializeInode writes an inode to the given writer
func (w *Writer) serializeInode(buf *bytes.Buffer, inode *builder.InodeData) error {
	// Determine inode format
	useExtended := needsExtendedInode(inode)

	if useExtended {
		return w.writeExtendedInodeToBuffer(buf, inode)
	}
	return w.writeCompactInodeToBuffer(buf, inode)
}

// needsExtendedInode determines if extended format is needed
func needsExtendedInode(inode *builder.InodeData) bool {
	node := inode.Node
	// Use extended if:
	// - Size > 4GB
	// - UID/GID > 65535
	// - Need to preserve nanosecond mtime
	return node.Size > 0xFFFFFFFF ||
		node.UID > 0xFFFF ||
		node.GID > 0xFFFF ||
		node.MtimeNs != 0
}

// writeCompactInodeToBuffer writes a compact (32-byte) inode to a buffer
func (w *Writer) writeCompactInodeToBuffer(buf *bytes.Buffer, inode *builder.InodeData) error {
	node := inode.Node

	format := uint16(inode.Layout)
	if len(inode.XattrData) > 0 {
		format |= (1 << 7) // Set xattr bit
	}

	mode := goFileModeToEroFSMode(node.Mode)

	inodeData := calculateInodeData(inode, inode.Layout, inode.BlockAddr)

	ic := disk.InodeCompact{
		Format:     format,
		XattrCount: uint16(len(node.Xattrs)),
		Mode:       mode,
		Nlink:      1,
		Size:       uint32(node.Size),
		Reserved:   0,
		InodeData:  inodeData,
		Inode:      uint32(inode.Nid),
		UID:        uint16(node.UID),
		GID:        uint16(node.GID),
		Reserved2:  0,
	}

	// Handle hardlinks
	if node.Hardlink != nil {
		ic.Nlink = 2 // Simplified - would need proper link counting
	}

	return binary.Write(buf, binary.LittleEndian, &ic)
}

// writeExtendedInodeToBuffer writes an extended (64-byte) inode to a buffer
func (w *Writer) writeExtendedInodeToBuffer(buf *bytes.Buffer, inode *builder.InodeData) error {
	node := inode.Node

	format := uint16(inode.Layout) | (1 << 8) // Extended format bit
	if len(inode.XattrData) > 0 {
		format |= (1 << 7) // Set xattr bit
	}

	mode := goFileModeToEroFSMode(node.Mode)

	inodeData := calculateInodeData(inode, inode.Layout, inode.BlockAddr)

	ie := disk.InodeExtended{
		Format:     format,
		XattrCount: uint16(len(node.Xattrs)),
		Mode:       mode,
		Reserved:   0,
		Size:       uint64(node.Size),
		InodeData:  inodeData,
		Inode:      uint32(inode.Nid),
		UID:        node.UID,
		GID:        node.GID,
		Mtime:      uint64(node.Mtime.Unix()),
		MtimeNs:    node.MtimeNs,
		Nlink:      1,
	}

	// Handle hardlinks
	if node.Hardlink != nil {
		ie.Nlink = 2 // Simplified
	}

	return binary.Write(buf, binary.LittleEndian, &ie)
}

// calculateInodeData computes the InodeData field based on file type
func calculateInodeData(inode *builder.InodeData, layout uint8, blockAddr uint32) uint32 {
	node := inode.Node
	switch {
	case node.Mode&fs.ModeCharDevice != 0 || node.Mode&fs.ModeDevice != 0:
		// Device number: major in upper 20 bits, minor in lower 12 bits
		return uint32(node.Devmajor<<12 | node.Devminor)
	case node.Mode.IsRegular() || node.Mode.IsDir() || node.Mode&fs.ModeSymlink != 0:
		// Block address for data
		return blockAddr
	default:
		return 0
	}
}

// goFileModeToEroFSMode converts Go's FileMode to EROFS mode
func goFileModeToEroFSMode(mode fs.FileMode) uint16 {
	var m uint16

	// Permission bits
	m |= uint16(mode & 0777)

	// File type
	switch mode & fs.ModeType {
	case 0:
		m |= disk.StatTypeReg
	case fs.ModeDir:
		m |= disk.StatTypeDir
	case fs.ModeSymlink:
		m |= disk.StatTypeSymlink
	case fs.ModeCharDevice:
		m |= disk.StatTypeChrdev
	case fs.ModeDevice:
		m |= disk.StatTypeBlkdev
	case fs.ModeNamedPipe:
		m |= disk.StatTypeFifo
	case fs.ModeSocket:
		m |= disk.StatTypeSock
	}

	// Special bits
	if mode&fs.ModeSetuid != 0 {
		m |= disk.StatTypeIsUID
	}
	if mode&fs.ModeSetgid != 0 {
		m |= disk.StatTypeIsGID
	}
	if mode&fs.ModeSticky != 0 {
		m |= disk.StatTypeIsVTX
	}

	return m
}

// writeDataBlocks writes all data blocks
func (w *Writer) writeDataBlocks(b *builder.Builder) error {
	blocks := b.GetDataBlocks()

	for _, block := range blocks {
		// Pad to reach the correct block address
		targetPos := int(block.Addr) * int(w.blockSize)
		currentPos := w.buffer.Len()

		if targetPos > currentPos {
			padding := targetPos - currentPos
			if err := w.writeZeros(padding); err != nil {
				return fmt.Errorf("padding to block %d: %w", block.Addr, err)
			}
		}

		// Write block data
		if _, err := w.buffer.Write(block.Data); err != nil {
			return fmt.Errorf("writing block at %d: %w", block.Addr, err)
		}

		// Align to block boundary
		if err := w.alignToBlock(); err != nil {
			return fmt.Errorf("aligning block %d: %w", block.Addr, err)
		}
	}

	return nil
}

// writeZeros writes n zero bytes
func (w *Writer) writeZeros(n int) error {
	zeros := make([]byte, n)
	_, err := w.buffer.Write(zeros)
	return err
}

// alignToBlock pads to the next block boundary
func (w *Writer) alignToBlock() error {
	currentPos := w.buffer.Len()
	remainder := currentPos % int(w.blockSize)
	if remainder > 0 {
		padding := int(w.blockSize) - remainder
		return w.writeZeros(padding)
	}
	return nil
}
