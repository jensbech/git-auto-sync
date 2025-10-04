_default:
    @just --list

build:
    go build -o git-auto-sync .
    cd daemon && go build -o git-auto-sync-daemon .

lint:
    #!/usr/bin/env bash
    if ! command -v golangci-lint &> /dev/null; then
        echo "golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
        exit 1
    fi
    golangci-lint run

test:
    go test ./...

install:
    go build -o git-auto-sync .
    cd daemon && go build -o git-auto-sync-daemon .
    sudo mv git-auto-sync /usr/local/bin/
    sudo mv daemon/git-auto-sync-daemon /usr/local/bin/

uninstall:
    sudo rm /usr/local/bin/git-auto-sync-daemon
    sudo rm /usr/local/bin/git-auto-sync

clean:
    rm -f git-auto-sync
    rm -f daemon/git-auto-sync-daemon

help:
    @just --list
