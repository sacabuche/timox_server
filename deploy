#!/bin/sh
set -e

BINARY=timox-server
REMOTE=freebox
REMOTE_DIR=/opt/timox

echo "Building for linux/arm64..."
BUILD_DATE=$(date -u +"%Y-%m-%d %H:%M UTC")
COMMIT_SHA=$(git rev-parse --short HEAD)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -ldflags "-X 'main.buildDate=$BUILD_DATE' -X 'main.commitSHA=$COMMIT_SHA'" \
  -o "$BINARY" .

echo "Stopping service on $REMOTE..."
ssh "$REMOTE" "rc-service timox status | grep -q started && doas rc-service timox stop || true"

echo "Copying binary to $REMOTE..."
scp "$BINARY" "$REMOTE:$REMOTE_DIR/$BINARY"

echo "Starting service on $REMOTE..."
ssh "$REMOTE" "doas rc-service timox start"

echo "Done."
rm "$BINARY"
