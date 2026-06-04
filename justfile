_default:
    @just --list

build:
    go build -o git-auto-sync .
    cd daemon && go build -o git-auto-sync-daemon .

build-app:
    cd macos/GitAutoSyncMenuBar && swift build -c release

build-all: build build-app

lint:
    #!/usr/bin/env bash
    if ! command -v golangci-lint &> /dev/null; then
        echo "golangci-lint not found. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"
        exit 1
    fi
    golangci-lint run

test:
    go test ./...

install: build
    sudo mv git-auto-sync /usr/local/bin/
    sudo mv daemon/git-auto-sync-daemon /usr/local/bin/

install-app: build-app
    #!/usr/bin/env bash
    set -euo pipefail
    app_dir="/Applications/Git Auto Sync.app"
    mkdir -p "$app_dir/Contents/MacOS"
    mkdir -p "$app_dir/Contents/Resources"
    cp macos/GitAutoSyncMenuBar/.build/release/GitAutoSyncMenuBar "$app_dir/Contents/MacOS/"
    cp assets/AppIcon.icns "$app_dir/Contents/Resources/AppIcon.icns"
    cat > "$app_dir/Contents/Info.plist" << 'PLIST'
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
        <key>CFBundleExecutable</key>
        <string>GitAutoSyncMenuBar</string>
        <key>CFBundleIdentifier</key>
        <string>com.gitautosync.menubar</string>
        <key>CFBundleName</key>
        <string>Git Auto Sync</string>
        <key>CFBundleIconFile</key>
        <string>AppIcon</string>
        <key>CFBundleVersion</key>
        <string>1.0</string>
        <key>CFBundleShortVersionString</key>
        <string>1.0</string>
        <key>LSMinimumSystemVersion</key>
        <string>14.0</string>
        <key>LSUIElement</key>
        <true/>
    </dict>
    </plist>
    PLIST
    touch "$app_dir"
    echo "Installed to $app_dir"

install-all: install install-app

uninstall:
    sudo rm -f /usr/local/bin/git-auto-sync-daemon
    sudo rm -f /usr/local/bin/git-auto-sync

uninstall-app:
    rm -rf "/Applications/Git Auto Sync.app"

uninstall-all: uninstall uninstall-app

run-app: build-app
    macos/GitAutoSyncMenuBar/.build/release/GitAutoSyncMenuBar

clean:
    rm -f git-auto-sync
    rm -f daemon/git-auto-sync-daemon
    cd macos/GitAutoSyncMenuBar && swift package clean

help:
    @just --list
