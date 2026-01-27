FROM alpine:latest

# Install required tools
RUN apk add --no-cache \
    tar \
    util-linux \
    e2fsprogs \
    kmod

# Create mount points
RUN mkdir -p /mnt/erofs /mnt/output

# Copy test script
COPY mount-and-extract.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/mount-and-extract.sh

ENTRYPOINT ["/usr/local/bin/mount-and-extract.sh"]
