#!/bin/bash

# Git Auto Sync - Authentication Setup Script
# This script configures Git to use GitHub CLI for authentication

set -e

echo "🔧 Setting up Git authentication for git-auto-sync..."

# Check if gh CLI is installed
if ! command -v gh &> /dev/null; then
    echo "❌ GitHub CLI (gh) is not installed."
    echo "Please install it first:"
    echo "  macOS: brew install gh"
    echo "  Linux: https://github.com/cli/cli/blob/trunk/docs/install_linux.md"
    echo "  Windows: https://github.com/cli/cli/releases"
    exit 1
fi

# Check if user is logged into GitHub CLI
if ! gh auth status &> /dev/null; then
    echo "❌ Not logged into GitHub CLI."
    echo "Please run: gh auth login"
    exit 1
fi

echo "✅ GitHub CLI is installed and authenticated"

# Configure Git to use GitHub CLI for authentication
echo "🔧 Configuring Git to use GitHub CLI for authentication..."
gh auth setup-git

echo "✅ Git authentication configured successfully!"
echo ""
echo "You can now use git-auto-sync with any GitHub repository."
echo "The tool will automatically use your GitHub CLI credentials."