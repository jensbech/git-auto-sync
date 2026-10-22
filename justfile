_default:
    @just --list

build:
    #!/usr/bin/env bash
    set -euo pipefail
    ver=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
    go build -ldflags "-X main.version=$ver" -o git-auto-sync .
    cd daemon && go build -ldflags "-X main.version=$ver" -o git-auto-sync-daemon .

build-app:
    cd macos/GitAutoSyncMenuBar && swift build -c release

test-app:
    cd macos/GitAutoSyncMenuBar && swift test

build-all: build build-app

lint:
    #!/usr/bin/env bash
    set -euo pipefail
    if command -v golangci-lint &> /dev/null; then
        golangci-lint run ./...
    else
        go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
    fi

test:
    go test ./...

fuzz time="15s":
    go test ./common -run '^$' -fuzz '^FuzzReadLine$' -fuzztime {{time}}
    go test ./common -run '^$' -fuzz '^FuzzDecodeAndDispatch$' -fuzztime {{time}}
    go test ./daemon -run '^$' -fuzz '^FuzzHandleCommand$' -fuzztime {{time}}

check:
    test -z "$(git ls-files -co --exclude-standard '*.go' | xargs gofmt -l)"
    go vet ./...
    go test -race ./...
    GOOS=linux go build ./...
    GOOS=windows go build ./...

install: build
    sudo mv git-auto-sync /usr/local/bin/
    sudo mv daemon/git-auto-sync-daemon /usr/local/bin/

bundle-app: build-app
    #!/usr/bin/env bash
    set -euo pipefail
    app_dir="macos/GitAutoSyncMenuBar/.build/Git Auto Sync.app"
    describe="$(git describe --tags --always --dirty 2>/dev/null || echo 0.0.0)"
    short_version="${describe#v}"
    build_number="$(git rev-list --count HEAD 2>/dev/null || echo 1)"
    rm -rf "$app_dir"
    mkdir -p "$app_dir/Contents/MacOS"
    mkdir -p "$app_dir/Contents/Resources"
    cp macos/GitAutoSyncMenuBar/.build/release/GitAutoSyncMenuBar "$app_dir/Contents/MacOS/"
    cp assets/AppIcon.icns "$app_dir/Contents/Resources/AppIcon.icns"
    cat > "$app_dir/Contents/Info.plist" << PLIST
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
        <key>CFBundlePackageType</key>
        <string>APPL</string>
        <key>CFBundleIconFile</key>
        <string>AppIcon</string>
        <key>CFBundleVersion</key>
        <string>${build_number}</string>
        <key>CFBundleShortVersionString</key>
        <string>${short_version}</string>
        <key>LSMinimumSystemVersion</key>
        <string>14.0</string>
        <key>LSUIElement</key>
        <true/>
        <key>NSUserNotificationAlertStyle</key>
        <string>alert</string>
    </dict>
    </plist>
    PLIST
    codesign --sign - --force --deep "$app_dir"
    echo "Bundled $app_dir ($short_version, build $build_number)"

install-app: bundle-app
    #!/usr/bin/env bash
    set -euo pipefail
    app_dir="/Applications/Git Auto Sync.app"
    rm -rf "$app_dir"
    ditto "macos/GitAutoSyncMenuBar/.build/Git Auto Sync.app" "$app_dir"
    touch "$app_dir"
    echo "Installed to $app_dir"

install-all: install install-app

uninstall:
    sudo rm -f /usr/local/bin/git-auto-sync-daemon
    sudo rm -f /usr/local/bin/git-auto-sync

uninstall-app:
    rm -rf "/Applications/Git Auto Sync.app"

uninstall-all: uninstall uninstall-app

run-app: bundle-app
    "macos/GitAutoSyncMenuBar/.build/Git Auto Sync.app/Contents/MacOS/GitAutoSyncMenuBar"

clean:
    rm -f git-auto-sync
    rm -f daemon/git-auto-sync-daemon daemon/daemon
    cd macos/GitAutoSyncMenuBar && swift package clean

help:
    @just --list
