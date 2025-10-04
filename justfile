# Default recipe - show available recipes
_default:
    @just --list

# Build both binaries
build:
    go build -o git-auto-sync .
    cd daemon && go build -o git-auto-sync-daemon .

# Run linter (check if golangci-lint is installed first)
lint:
    #!/usr/bin/env bash
    if ! command -v golangci-lint &> /dev/null; then
        echo "golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
        exit 1
    fi
    golangci-lint run

# Run tests
test:
    go test ./...

# Build and install both binaries
install:
    go build -o git-auto-sync .
    cd daemon && go build -o git-auto-sync-daemon .
    sudo mv git-auto-sync /usr/local/bin/
    sudo mv daemon/git-auto-sync-daemon /usr/local/bin/

# Clean up built binaries
clean:
    rm -f git-auto-sync
    rm -f daemon/git-auto-sync-daemon

# Show available recipes
help:
    @just --list