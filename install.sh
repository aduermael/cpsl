#!/bin/bash
set -e

# Detect OS and ARCH
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  linux)
    OS_NAME="linux"
    ;;
  darwin)
    OS_NAME="darwin"
    ;;
  *)
    echo "Unsupported OS: $OS"
    exit 1
    ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)
    ARCH_NAME="amd64"
    ;;
  aarch64|arm64)
    ARCH_NAME="arm64"
    ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

# Installation directory (default: /usr/local/bin)
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Check if we have write permissions
if [ ! -w "$INSTALL_DIR" ]; then
  echo "Error: Cannot write to $INSTALL_DIR. Try running with sudo or set INSTALL_DIR."
  exit 1
fi

# Fetch the latest release tag from GitHub API
echo "Fetching latest release information..."
RELEASE_INFO=$(curl -s https://api.github.com/repos/aduermael/herm/releases/latest)
LATEST_TAG=$(echo "$RELEASE_INFO" | grep -o '"tag_name":"[^"]*' | head -1 | cut -d'"' -f4)

if [ -z "$LATEST_TAG" ]; then
  echo "Error: Could not determine latest release tag from GitHub API"
  exit 1
fi

# Remove 'v' prefix if present
VERSION="${LATEST_TAG#v}"

echo "Latest version: $LATEST_TAG"

# Construct download URL (goreleaser archive naming: herm_version_os_arch.tar.gz without 'v' in version)
DOWNLOAD_URL="https://github.com/aduermael/herm/releases/download/${LATEST_TAG}/herm_${VERSION}_${OS_NAME}_${ARCH_NAME}.tar.gz"

echo "Downloading from: $DOWNLOAD_URL"

# Create temporary directory for extraction
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

# Download the archive
curl -L -o "$TEMP_DIR/herm.tar.gz" "$DOWNLOAD_URL"

if [ ! -f "$TEMP_DIR/herm.tar.gz" ]; then
  echo "Error: Failed to download release"
  exit 1
fi

# Extract the archive
echo "Extracting..."
tar -xzf "$TEMP_DIR/herm.tar.gz" -C "$TEMP_DIR"

# Check if binary exists
if [ ! -f "$TEMP_DIR/herm" ]; then
  echo "Error: Binary 'herm' not found in archive"
  exit 1
fi

# Make it executable
chmod +x "$TEMP_DIR/herm"

# Install to target directory
echo "Installing to $INSTALL_DIR..."
cp "$TEMP_DIR/herm" "$INSTALL_DIR/herm"

# Verify the binary works
echo "Verifying installation..."
if ! "$INSTALL_DIR/herm" --version > /dev/null 2>&1; then
  echo "Warning: Could not verify binary (may need to set up API keys)"
fi

echo "✓ Installation complete!"
echo "herm installed to: $INSTALL_DIR/herm"
echo "Version: $LATEST_TAG"
