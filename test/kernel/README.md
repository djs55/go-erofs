# EROFS Kernel Testing

This directory contains a Docker-based test harness to verify that EROFS images created by go-erofs can be mounted and read by the Linux kernel.

## How it Works

1. **Test orchestration** (`kernel_test.go`):
   - Creates a test tar with various file types
   - Converts tar to EROFS using `MakeFromTar()`
   - Builds Docker image with kernel support
   - Runs container in privileged mode to mount EROFS
   - Compares extracted tar with original

2. **Docker container** (`Dockerfile`):
   - Alpine Linux base (small, has EROFS kernel support)
   - Mounts EROFS image via loop device
   - Extracts contents to tar
   - Returns extracted tar to host

3. **Mount script** (`mount-and-extract.sh`):
   - Sets up loop device
   - Mounts EROFS filesystem
   - Extracts to tar
   - Cleans up

## Requirements

- Docker installed and running
- Privileged container access (for mounting filesystems)
- Linux kernel with EROFS support (most recent kernels)

## Running Tests

```bash
# Run kernel tests
go test -v -run TestKernelMount

# Skip kernel tests
SKIP_KERNEL_TESTS=1 go test ./...
```

## Why This Matters

The kernel test catches bugs that only appear when the Linux kernel tries to mount the filesystem:

- **Format field encoding**: Our reader was lenient, but kernel rejected wrong format bits
- **Permission bits**: Ensures mode bits are correctly encoded
- **File size limits**: Validates block boundary handling
- **Device nodes**: Tests major/minor number encoding
- **Extended attributes**: Verifies xattr storage format

## Troubleshooting

If tests fail:

1. **Check Docker**: `docker ps` should work
2. **Check privileges**: Container needs `--privileged` flag
3. **Check kernel support**: `grep EROFS /boot/config-$(uname -r)` (on Linux)
4. **View logs**: Test output includes mount errors and dmesg

## Example Output

```
=== RUN   TestKernelMount
Setting up loop device...
Loop device: /dev/loop0
Mounting EROFS filesystem...
Mount successful! Listing contents...
Extracting to tar...
Done! Output written to /mnt/output/output.tar
Kernel test passed! Linux kernel successfully mounted and read EROFS image
--- PASS: TestKernelMount (2.34s)
```
