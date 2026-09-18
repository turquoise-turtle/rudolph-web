#!/usr/bin/env bash
set -e

# Figure out the project's basepath
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do SOURCE="$(readlink "$SOURCE")"; done
DIR="$(cd -P "$(dirname "$SOURCE")/.." && pwd)"

# Establish directories
BUILD_DIR=$DIR/build
LINUX_BUILD_DIR=$BUILD_DIR/linux/web
PKG_DIR=$BUILD_DIR/package
DEPLOYMENT_ZIP_PATH=$PKG_DIR/rudolph_web_deployment.zip

cd "$DIR"

# Cleanup from previous run(s)
rm -rf "$BUILD_DIR"

echo "*** compiling application binary... ***"

# provided.al2023 on arm64 runs a statically linked binary named bootstrap. lambda.norpc drops the
# unused RPC server from aws-lambda-go.
echo "  compiling web in linux:arm64..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" \
    -o "$LINUX_BUILD_DIR/bootstrap" "$DIR/cmd/lambda"

echo "*** packaging... ***"

mkdir -p "$PKG_DIR"
# zip from inside the build dir so the binary sits at the root of the archive.
cd "$LINUX_BUILD_DIR"
zip -q -r "$DEPLOYMENT_ZIP_PATH" ./*

echo "*** complete ***"
echo "  created:"
echo "    Lambda: $DEPLOYMENT_ZIP_PATH"
