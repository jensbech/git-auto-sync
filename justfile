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

# Rename default branch from master to main (idempotent)
rename-branch:
    #!/usr/bin/env bash
    set -euo pipefail
    current_branch="$(git symbolic-ref --short HEAD)"
    if [ "$current_branch" = "master" ]; then
        echo "Renaming local branch master -> main"
        git branch -m master main
    fi
    if git show-ref --verify --quiet refs/heads/main; then
        echo "Pushing main (setting upstream)"
        git push -u origin main || true
    fi
    echo "Setting GitHub default branch to main (requires gh CLI auth)"
    if command -v gh >/dev/null 2>&1; then
        gh repo edit --default-branch main || true
    else
        echo "gh CLI not installed; skip remote default branch change"
    fi
    echo "Done. Update any open PR base branches manually if needed."
