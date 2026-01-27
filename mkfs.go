package erofs

import (
	"fmt"
	"io"
	"time"

	"github.com/erofs/go-erofs/builder"
	"github.com/erofs/go-erofs/tarfs"
	"github.com/erofs/go-erofs/writer"
)

// Config holds configuration for filesystem creation
type Config struct {
	BlockSize uint8      // Block size in bits (default 12 = 4KB)
	ForceUID  *uint32    // Override all UIDs
	ForceGID  *uint32    // Override all GIDs
	Timestamp *time.Time // Override build timestamp
	UUID      *[16]byte  // Filesystem UUID
}

// Option is a functional option for Config
type Option func(*Config)

// WithBlockSize sets the block size in bits (e.g., 12 for 4KB blocks)
func WithBlockSize(bits uint8) Option {
	return func(c *Config) {
		c.BlockSize = bits
	}
}

// WithForceUID overrides all UIDs with the given value
func WithForceUID(uid uint32) Option {
	return func(c *Config) {
		c.ForceUID = &uid
	}
}

// WithForceGID overrides all GIDs with the given value
func WithForceGID(gid uint32) Option {
	return func(c *Config) {
		c.ForceGID = &gid
	}
}

// WithTimestamp sets a fixed build timestamp (for reproducible builds)
func WithTimestamp(ts time.Time) Option {
	return func(c *Config) {
		c.Timestamp = &ts
	}
}

// WithUUID sets the filesystem UUID
func WithUUID(uuid [16]byte) Option {
	return func(c *Config) {
		c.UUID = &uuid
	}
}

// MakeFromTar creates an EROFS filesystem from a tar archive
func MakeFromTar(tarReader io.Reader, out io.Writer, opts ...Option) error {
	// Apply default config
	config := &Config{
		BlockSize: 12, // 4KB blocks
	}
	for _, opt := range opts {
		opt(config)
	}

	// Parse tar archive
	tree, err := tarfs.FromTar(tarReader)
	if err != nil {
		return fmt.Errorf("parsing tar: %w", err)
	}

	// Apply UID/GID overrides if requested
	if config.ForceUID != nil || config.ForceGID != nil {
		applyUIDGIDOverrides(tree, config.ForceUID, config.ForceGID)
	}

	// Build filesystem structures
	b := builder.New(tree, config.BlockSize)
	if err := b.Build(); err != nil {
		return fmt.Errorf("building filesystem: %w", err)
	}

	// Determine build timestamp
	buildTime := time.Now()
	if config.Timestamp != nil {
		buildTime = *config.Timestamp
	}

	// Write filesystem
	w := writer.New(out, config.BlockSize)
	if err := w.WriteFS(b, buildTime, config.UUID); err != nil {
		return fmt.Errorf("writing filesystem: %w", err)
	}

	return nil
}

// applyUIDGIDOverrides recursively applies UID/GID overrides to all nodes
func applyUIDGIDOverrides(tree *tarfs.Tree, forceUID, forceGID *uint32) {
	for _, node := range tree.Nodes {
		if forceUID != nil {
			node.UID = *forceUID
		}
		if forceGID != nil {
			node.GID = *forceGID
		}
	}
}
