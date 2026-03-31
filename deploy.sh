#!/bin/sh
set -e

BINARY=timox-server
REMOTE=freebox
REMOTE_DIR=/opt/timox

echo "Building for linux/amd64..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o "$BINARY" .

echo "Copying binary to $REMOTE..."
scp "$BINARY" "$REMOTE:$REMOTE_DIR/$BINARY"

echo "Restarting service..."
ssh "$REMOTE" "doas rc-service timox restart"

echo "Done."
rm "$BINARY"
