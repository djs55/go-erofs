#!/bin/sh
set -e

if [ ! -f "/mnt/input/test.erofs" ]; then
    echo "Error: /mnt/input/test.erofs not found"
    exit 1
fi

echo "Setting up loop device..."
LOOP_DEV=$(losetup -f)
losetup "$LOOP_DEV" /mnt/input/test.erofs

echo "Loop device: $LOOP_DEV"
losetup -l

echo "Mounting EROFS filesystem..."
if ! mount -t erofs "$LOOP_DEV" /mnt/erofs 2>&1; then
    echo "Mount failed, trying to get more info..."
    dmesg | tail -20
    losetup -d "$LOOP_DEV" || true
    exit 1
fi

echo "Mount successful! Listing contents..."
ls -la /mnt/erofs/

echo "Extracting to tar..."
cd /mnt/erofs
tar -cf /mnt/output/output.tar .

echo "Cleaning up..."
cd /
umount /mnt/erofs
losetup -d "$LOOP_DEV"

echo "Done! Output written to /mnt/output/output.tar"
