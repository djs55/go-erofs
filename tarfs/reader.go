package tarfs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// Node represents a single entry from a tar archive
type Node struct {
	Path     string
	Linkname string
	Mode     fs.FileMode
	UID      uint32
	GID      uint32
	Size     int64
	Mtime    time.Time
	MtimeNs  uint32
	Devmajor uint64
	Devminor uint64
	Xattrs   map[string]string
	Data     io.Reader
	RawData  []byte // raw file content for creating new readers
	Hardlink *Node  // points to original for hardlinks
	Children map[string]*Node
}

// NewReader creates a new reader for the node's data
func (n *Node) NewReader() io.Reader {
	if n.Hardlink != nil {
		return n.Hardlink.NewReader()
	}
	if n.RawData != nil {
		return bytes.NewReader(n.RawData)
	}
	return nil
}

// Tree represents the complete filesystem tree from a tar archive
type Tree struct {
	Root      *Node
	Nodes     map[string]*Node
	Hardlinks map[string]*Node
}

// FromTar reads a tar archive and constructs a filesystem tree
func FromTar(r io.Reader) (*Tree, error) {
	tr := tar.NewReader(r)
	tree := &Tree{
		Nodes:     make(map[string]*Node),
		Hardlinks: make(map[string]*Node),
	}

	// Create root node
	tree.Root = &Node{
		Path:     "",
		Mode:     fs.ModeDir | 0755,
		Children: make(map[string]*Node),
	}
	tree.Nodes[""] = tree.Root
	tree.Nodes["."] = tree.Root

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		// Validate and clean path
		cleanPath := path.Clean(header.Name)
		if path.IsAbs(cleanPath) {
			return nil, fmt.Errorf("absolute path not allowed: %s", header.Name)
		}
		if strings.Contains(cleanPath, "..") {
			return nil, fmt.Errorf("path with .. not allowed: %s", header.Name)
		}

		// Create node
		node := &Node{
			Path:     cleanPath,
			Linkname: header.Linkname,
			UID:      uint32(header.Uid),
			GID:      uint32(header.Gid),
			Size:     header.Size,
			Mtime:    header.ModTime,
			Devmajor: uint64(header.Devmajor),
			Devminor: uint64(header.Devminor),
			Xattrs:   make(map[string]string),
		}

		// Extract nanoseconds from ModTime
		node.MtimeNs = uint32(header.ModTime.Nanosecond())

		// Convert tar type to FileMode
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			node.Mode = fs.FileMode(header.Mode) & 0777
			// Store data for regular files
			data := make([]byte, header.Size)
			if _, err := io.ReadFull(tr, data); err != nil {
				return nil, fmt.Errorf("reading file data for %s: %w", cleanPath, err)
			}
			node.RawData = data
			node.Data = bytes.NewReader(data)
		case tar.TypeDir:
			node.Mode = fs.ModeDir | (fs.FileMode(header.Mode) & 0777)
			node.Children = make(map[string]*Node)
		case tar.TypeSymlink:
			node.Mode = fs.ModeSymlink | 0777
		case tar.TypeLink:
			// Hardlink - store reference to be resolved later
			tree.Hardlinks[cleanPath] = node
			node.Mode = 0 // Will be set when resolved
		case tar.TypeChar:
			node.Mode = fs.ModeCharDevice | (fs.FileMode(header.Mode) & 0777)
		case tar.TypeBlock:
			node.Mode = fs.ModeDevice | (fs.FileMode(header.Mode) & 0777)
		case tar.TypeFifo:
			node.Mode = fs.ModeNamedPipe | (fs.FileMode(header.Mode) & 0777)
		default:
			return nil, fmt.Errorf("unsupported tar type %c for %s", header.Typeflag, cleanPath)
		}

		// Extract PAX xattrs
		if header.PAXRecords != nil {
			extractXattrs(header.PAXRecords, node.Xattrs)
		}

		// Add node to tree
		tree.Nodes[cleanPath] = node

		// Create intermediate directories and link to parent
		if err := linkToParent(tree, node); err != nil {
			return nil, err
		}
	}

	// Resolve hardlinks
	if err := resolveHardlinks(tree); err != nil {
		return nil, err
	}

	return tree, nil
}

// extractXattrs extracts xattrs from PAX records
func extractXattrs(pax map[string]string, xattrs map[string]string) {
	for key, value := range pax {
		// SCHILY.xattr.name or LIBARCHIVE.xattr.name
		if strings.HasPrefix(key, "SCHILY.xattr.") {
			xattrName := strings.TrimPrefix(key, "SCHILY.xattr.")
			xattrs[xattrName] = value
		} else if strings.HasPrefix(key, "LIBARCHIVE.xattr.") {
			xattrName := strings.TrimPrefix(key, "LIBARCHIVE.xattr.")
			xattrs[xattrName] = value
		}
	}
}

// linkToParent creates intermediate directories and links node to its parent
func linkToParent(tree *Tree, node *Node) error {
	if node.Path == "" || node.Path == "." {
		return nil
	}

	parentPath := path.Dir(node.Path)
	if parentPath == "." {
		parentPath = ""
	}

	// Ensure parent exists
	parent, exists := tree.Nodes[parentPath]
	if !exists {
		// Create intermediate directory
		parent = &Node{
			Path:     parentPath,
			Mode:     fs.ModeDir | 0755,
			Children: make(map[string]*Node),
		}
		tree.Nodes[parentPath] = parent

		// Recursively link parent
		if err := linkToParent(tree, parent); err != nil {
			return err
		}
	}

	// Add to parent's children
	if parent.Children == nil {
		parent.Children = make(map[string]*Node)
	}
	basename := path.Base(node.Path)
	parent.Children[basename] = node

	return nil
}

// resolveHardlinks resolves hardlink references to their target nodes
func resolveHardlinks(tree *Tree) error {
	for linkPath, linkNode := range tree.Hardlinks {
		targetPath := path.Clean(linkNode.Linkname)
		target, exists := tree.Nodes[targetPath]
		if !exists {
			return fmt.Errorf("hardlink %s points to non-existent target %s", linkPath, targetPath)
		}
		if target.Mode&fs.ModeType != 0 {
			return fmt.Errorf("hardlink %s target %s is not a regular file", linkPath, targetPath)
		}

		// Point to the same data
		linkNode.Hardlink = target
		linkNode.Mode = target.Mode
		linkNode.Size = target.Size
		linkNode.RawData = target.RawData
		linkNode.Data = bytes.NewReader(target.RawData)
	}
	return nil
}
