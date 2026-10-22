import SwiftUI
import AppKit
import UniformTypeIdentifiers

enum PathFormat {
    static func abbreviate(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path == home { return "~" }
        if path.hasPrefix(home + "/") { return "~" + path.dropFirst(home.count) }
        return path
    }

    static func shellQuoted(_ path: String) -> String {
        "'" + path.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}

enum TerminalApp: String, CaseIterable, Identifiable {
    case terminal, iterm, ghostty, warp

    static let storageKey = "gas.terminalApp"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .terminal: return "Terminal"
        case .iterm: return "iTerm"
        case .ghostty: return "Ghostty"
        case .warp: return "Warp"
        }
    }

    var bundleIdentifier: String {
        switch self {
        case .terminal: return "com.apple.Terminal"
        case .iterm: return "com.googlecode.iterm2"
        case .ghostty: return "com.mitchellh.ghostty"
        case .warp: return "dev.warp.Warp-Stable"
        }
    }

    var applicationURL: URL? {
        NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleIdentifier)
    }

    var isInstalled: Bool { applicationURL != nil }

    static var preferred: TerminalApp {
        UserDefaults.standard.string(forKey: storageKey).flatMap(TerminalApp.init(rawValue:)) ?? .terminal
    }
}

@MainActor
enum RepoActions {
    static func reveal(_ path: String) {
        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
    }

    static func revealParent(_ path: String) {
        var url = URL(fileURLWithPath: path).deletingLastPathComponent()
        while url.path != "/", !FileManager.default.fileExists(atPath: url.path) {
            url.deleteLastPathComponent()
        }
        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: url.path)
    }

    static func openInTerminal(_ path: String) {
        let folder = URL(fileURLWithPath: path, isDirectory: true)
        let app = TerminalApp.preferred
        if app == .warp, let url = warpURL(for: path), app.isInstalled {
            NSWorkspace.shared.open(url)
            return
        }
        let appURL = app.applicationURL ?? TerminalApp.terminal.applicationURL
        guard let appURL else { return }
        NSWorkspace.shared.open([folder], withApplicationAt: appURL, configuration: NSWorkspace.OpenConfiguration())
    }

    static func openFile(_ file: String, in repoPath: String) {
        let url = URL(fileURLWithPath: repoPath).appendingPathComponent(file)
        if FileManager.default.fileExists(atPath: url.path) {
            NSWorkspace.shared.open(url)
        } else {
            reveal(repoPath)
        }
    }

    static func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    static func chooseRepositories() -> [URL] {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = true
        panel.prompt = "Add"
        panel.message = "Select Git repositories to monitor"
        guard panel.runModal() == .OK else { return [] }
        return panel.urls
    }

    @discardableResult
    static func addRepositories(_ appState: AppState) -> Bool {
        let urls = chooseRepositories()
        guard !urls.isEmpty else { return false }
        Task { await appState.addRepos(urls) }
        return true
    }

    static func handleDrop(_ providers: [NSItemProvider], appState: AppState) -> Bool {
        let fileProviders = providers.filter { $0.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) }
        guard !fileProviders.isEmpty else { return false }
        Task {
            var urls: [URL] = []
            for provider in fileProviders {
                if let url = await droppedFolder(provider) { urls.append(url) }
            }
            if !urls.isEmpty { await appState.addRepos(urls) }
        }
        return true
    }

    private static func droppedFolder(_ provider: NSItemProvider) async -> URL? {
        await withCheckedContinuation { continuation in
            _ = provider.loadObject(ofClass: URL.self) { url, _ in
                guard let url else {
                    continuation.resume(returning: nil)
                    return
                }
                var isDir: ObjCBool = false
                let isFolder = FileManager.default.fileExists(atPath: url.path, isDirectory: &isDir) && isDir.boolValue
                continuation.resume(returning: isFolder ? url : nil)
            }
        }
    }

    static func resolveCommands(for repo: RepoState) -> String {
        var lines = ["cd \(PathFormat.shellQuoted(repo.path))", "git status", "git pull --rebase"]
        if let files = repo.conflictFiles, !files.isEmpty {
            lines.append("# resolve conflicts in: " + files.joined(separator: ", "))
        } else {
            lines.append("# resolve the conflicting files, then:")
        }
        lines.append("git add -A && git rebase --continue")
        lines.append("git push")
        return lines.joined(separator: "\n")
    }

    private static func warpURL(for path: String) -> URL? {
        var components = URLComponents()
        components.scheme = "warp"
        components.host = "action"
        components.path = "/new_tab"
        components.queryItems = [URLQueryItem(name: "path", value: path)]
        return components.url
    }
}

struct CommandSnippet: View {
    let command: String
    @State private var copied = false

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Text(command)
                .font(.system(size: 11, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button {
                RepoActions.copy(command)
                copied = true
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .font(.system(size: 11))
                    .frame(width: 16, height: 16)
            }
            .buttonStyle(.borderless)
            .help(copied ? "Copied" : "Copy command")
            .accessibilityLabel(copied ? "Copied" : "Copy command")
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(Color.primary.opacity(0.06))
        )
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(2))
            copied = false
        }
    }
}

struct RemoveRepoConfirmation: ViewModifier {
    @Binding var repo: RepoState?
    let appState: AppState

    func body(content: Content) -> some View {
        content.confirmationDialog(
            "Stop monitoring \(repo?.name ?? "this repository")?",
            isPresented: Binding(
                get: { repo != nil },
                set: { if !$0 { repo = nil } }
            ),
            titleVisibility: .visible,
            presenting: repo
        ) { target in
            Button("Stop Monitoring", role: .destructive) {
                Task { await appState.removeRepo(target.path) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { target in
            Text("Files in \(PathFormat.abbreviate(target.path)) stay untouched. Git Auto Sync just stops committing, pulling, and pushing this repository.")
        }
    }
}

extension View {
    func confirmRemoval(of repo: Binding<RepoState?>, appState: AppState) -> some View {
        modifier(RemoveRepoConfirmation(repo: repo, appState: appState))
    }
}
