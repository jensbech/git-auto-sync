import SwiftUI
import AppKit

struct IssueBanner<Content: View>: View {
    let tint: Color
    let symbol: String
    let title: String
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: symbol)
                    .foregroundStyle(tint)
                    .accessibilityHidden(true)
                Text(title)
                    .font(.system(size: 13, weight: .semibold))
                    .accessibilityAddTraits(.isHeader)
                Spacer()
            }
            content
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(tint.opacity(0.09))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(tint.opacity(0.35), lineWidth: 0.7)
        )
        .accessibilityElement(children: .contain)
    }
}

private struct BannerText: View {
    let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text)
            .font(.system(size: 12))
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}

struct RepoIssueBanner: View {
    let appState: AppState
    let repo: RepoState
    let issue: RepoIssue
    @State private var pendingRemoval: RepoState?

    var body: some View {
        Group {
            switch issue {
            case .conflict(let files):
                conflictBanner(files)
            case .preflight(let kind, let message):
                preflightBanner(kind, message: message)
            case .offline(let message, let nextRetry):
                offlineBanner(message: message, nextRetry: nextRetry)
            case .error(let code, let message):
                errorBanner(code: code, message: message)
            }
        }
        .confirmRemoval(of: $pendingRemoval, appState: appState)
    }

    private func conflictBanner(_ files: [String]) -> some View {
        let style = StatusStyle.of(.conflict)
        return IssueBanner(tint: style.color, symbol: style.symbol, title: "Your changes conflict with the remote") {
            BannerText("Syncing stopped before anything was overwritten. Your work is committed locally and won't be pushed until the conflict is resolved.")
            if !files.isEmpty {
                VStack(alignment: .leading, spacing: 3) {
                    Text(files.count == 1 ? "Conflicting file" : "Conflicting files")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundStyle(.secondary)
                    ForEach(files, id: \.self) { file in
                        Button {
                            RepoActions.openFile(file, in: repo.path)
                        } label: {
                            HStack(spacing: 6) {
                                Image(systemName: "doc.text")
                                    .font(.system(size: 10))
                                    .foregroundStyle(style.color)
                                    .accessibilityHidden(true)
                                Text(file)
                                    .font(.system(size: 11, design: .monospaced))
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                        }
                        .buttonStyle(.plain)
                        .help("Open \(file)")
                        .accessibilityLabel("Open \(file)")
                    }
                }
            }
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) {
                    conflictPrimaryButtons(files)
                    copyResolveButton
                    Spacer(minLength: 0)
                }
                VStack(alignment: .leading, spacing: 6) {
                    HStack(spacing: 6) { conflictPrimaryButtons(files) }
                    copyResolveButton
                }
            }
            .controlSize(.small)
        }
    }

    @ViewBuilder
    private func conflictPrimaryButtons(_ files: [String]) -> some View {
        Button("Retry Sync") {
            Task { await appState.syncNow(repo.path, force: true) }
        }
        .help("Try the rebase again now, for example after you resolved it by hand")
        .disabled(repo.paused)
        Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
        Button("Open in Editor") {
            if let first = files.first { RepoActions.openFile(first, in: repo.path) }
        }
        .disabled(files.isEmpty)
        .help(files.first.map { "Open \($0) in its default app" } ?? "No conflicting files reported")
    }

    private var copyResolveButton: some View {
        Button("Copy Resolve Commands") {
            RepoActions.copy(RepoActions.resolveCommands(for: repo))
        }
        .help("Copy the shell commands to redo the rebase and resolve it by hand")
    }

    @ViewBuilder
    private func preflightBanner(_ kind: PreflightKind, message: String) -> some View {
        let style = StatusStyle.of(.preflight)
        switch kind {
        case .rebaseInProgress:
            IssueBanner(tint: style.color, symbol: style.symbol, title: "A rebase is in progress") {
                BannerText("Syncing is on hold until the rebase is finished or aborted. Aborting returns the branch to where it was before the rebase started.")
                HStack(spacing: 6) {
                    Button("Abort Rebase") {
                        Task { await appState.abortRebase(repo.path) }
                    }
                    .help("Run git rebase --abort in this repository")
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .mergeInProgress:
            IssueBanner(tint: style.color, symbol: style.symbol, title: "A merge is in progress") {
                BannerText("Syncing is on hold until the merge is committed or aborted. To abort it, run:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) merge --abort")
                HStack(spacing: 6) {
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .noUpstream:
            let branch = repo.branch.flatMap { $0.isEmpty ? nil : $0 } ?? "<branch>"
            IssueBanner(tint: style.color, symbol: style.symbol, title: "No upstream branch") {
                BannerText("“\(branch)” doesn't track a remote branch, so there is nowhere to push. Set the upstream:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) branch -u origin/\(branch)")
                BannerText("If the branch doesn't exist on the remote yet, push it once instead:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) push -u origin \(branch)")
                HStack(spacing: 6) {
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .upstreamGone:
            let branch = repo.branch.flatMap { $0.isEmpty ? nil : $0 } ?? "<branch>"
            let upstream = repo.upstream.flatMap { $0.isEmpty ? nil : $0 }
            IssueBanner(tint: style.color, symbol: "arrow.triangle.branch", title: "Remote branch was deleted") {
                BannerText("\(upstream.map { "“\($0)”" } ?? "The upstream branch") no longer exists on the remote, so there is nowhere to push. Your local commits are safe. To publish “\(branch)” again, run:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) push -u origin HEAD:\(branch)")
                BannerText("Or point the branch at another remote branch:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) branch -u origin/<other-branch>")
                HStack(spacing: 6) {
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .untrustedRepo:
            IssueBanner(tint: style.color, symbol: "lock.shield", title: "Git doesn't trust this folder") {
                BannerText("The folder is owned by a different user, so git refuses to work in it (its “dubious ownership” check). If you trust it, add it as a safe directory:")
                CommandSnippet(command: "git config --global --add safe.directory \(PathFormat.shellQuoted(repo.path))")
                HStack(spacing: 6) {
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .detachedHead:
            IssueBanner(tint: style.color, symbol: style.symbol, title: "Not on a branch") {
                BannerText("HEAD is detached, so there is no branch to sync. Check out a branch to resume:")
                CommandSnippet(command: "git -C \(PathFormat.shellQuoted(repo.path)) switch <branch>")
                HStack(spacing: 6) {
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        case .repoMissing:
            IssueBanner(tint: style.color, symbol: "folder.badge.questionmark", title: "Repository folder not found") {
                BannerText("\(PathFormat.abbreviate(repo.path)) was moved or deleted. Syncing resumes on its own if the folder comes back. If you moved it, add the new location and remove this one.")
                HStack(spacing: 6) {
                    Button("Remove from Git Auto Sync…") { pendingRemoval = repo }
                    Button("Reveal Parent in Finder") { RepoActions.revealParent(repo.path) }
                        .help("Show the folder that used to contain \(repo.name)")
                    Spacer()
                }
                .controlSize(.small)
            }
        case .other:
            IssueBanner(tint: style.color, symbol: style.symbol, title: "Syncing is on hold") {
                BannerText(ErrorClean.short(message).sentenceCased)
                HStack(spacing: 6) {
                    Button("Open in Finder") { RepoActions.reveal(repo.path) }
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    checkAgainButton
                    Spacer()
                }
                .controlSize(.small)
            }
        }
    }

    private var checkAgainButton: some View {
        Button("Check Again") {
            Task { await appState.syncNow(repo.path) }
        }
        .disabled(repo.paused)
        .help("Re-run the checks and sync now")
    }

    private func offlineBanner(message: String?, nextRetry: Date?) -> some View {
        let style = StatusStyle.of(.offline)
        return IssueBanner(tint: style.color, symbol: style.symbol, title: "Offline") {
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                BannerText(retryText(nextRetry, now: ctx.date))
            }
            if let message, !message.isEmpty {
                Text(ErrorClean.short(message))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .help(ErrorClean.short(message))
            }
            HStack(spacing: 6) {
                Button("Retry Now") {
                    Task { await appState.syncNow(repo.path) }
                }
                .disabled(repo.paused)
                Spacer()
            }
            .controlSize(.small)
        }
    }

    private func retryText(_ nextRetry: Date?, now: Date) -> String {
        let base = "Can't reach the remote. Your changes are still committed locally and will be pushed once it's reachable."
        if let nextRetry, let left = Durations.coarseCountdown(to: nextRetry, now: now) {
            return base + " Next try in \(left)."
        }
        return base + " Trying again shortly."
    }

    private func errorBanner(code: String?, message: String) -> some View {
        let guidance = ErrorGuidance.of(code: code, message: message, repoPath: repo.path)
        return IssueBanner(tint: StatusStyle.of(.error).color, symbol: "xmark.octagon.fill", title: guidance.title) {
            if let hint = guidance.hint {
                BannerText(hint)
            }
            if let command = guidance.command {
                CommandSnippet(command: command)
            }
            if guidance.showsMessage, !message.isEmpty {
                if guidance.hint == nil, ErrorClean.isSummary(message) {
                    SummaryError(message: message)
                } else {
                    ErrorDetails(message: message)
                }
            }
            HStack(spacing: 6) {
                Button("Retry") {
                    Task { await appState.syncNow(repo.path) }
                }
                .disabled(repo.paused)
                if guidance.offersTerminal {
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                }
                Spacer()
            }
            .controlSize(.small)
        }
    }
}

struct ErrorGuidance {
    let title: String
    var hint: String?
    var command: String?
    var offersTerminal = false
    var showsMessage = true

    static func of(code: String?, message: String = "", repoPath: String) -> ErrorGuidance {
        switch code {
        case "xcode_license":
            return ErrorGuidance(
                title: "Xcode license not accepted",
                hint: "Git can't run until the Xcode license is accepted. Run this in a terminal, then retry:",
                command: "sudo xcodebuild -license accept",
                offersTerminal: true,
                showsMessage: false
            )
        case "auth":
            return ErrorGuidance(
                title: "Authentication failed",
                hint: "The remote rejected your credentials. Check your SSH key or credential helper (for GitHub, run gh auth status), then retry.",
                offersTerminal: true
            )
        case "no_author":
            return ErrorGuidance(
                title: "Git author not configured",
                hint: "Git needs a name and email for commits. Set them, then retry:",
                command: "git config --global user.name \"Your Name\" && git config --global user.email \"you@example.com\"",
                offersTerminal: true
            )
        case "git_missing":
            return ErrorGuidance(
                title: "Git not found",
                hint: "Install the command line tools, then retry:",
                command: "xcode-select --install",
                offersTerminal: true
            )
        case "lock":
            return ErrorGuidance(
                title: "Repository is locked",
                hint: "Another git process holds the index lock. If no git command is running, remove the stale lock file:",
                command: "rm \(PathFormat.shellQuoted(repoPath + "/.git/index.lock"))",
                offersTerminal: true
            )
        case "push_rejected":
            return ErrorGuidance(
                title: "Push rejected",
                hint: "The remote refused the push. The branch may be protected, or a hook declined it."
            )
        case "timeout":
            return ErrorGuidance(
                title: "Git timed out",
                hint: "A git command took too long and was stopped. It will be retried."
            )
        case "network":
            return ErrorGuidance(
                title: "Network problem",
                hint: "The remote couldn't be reached. It will be retried."
            )
        case "config":
            let title = message.lowercased().hasPrefix("commit") ? "Commit failed" : "Sync failed"
            return ErrorGuidance(title: title, offersTerminal: true)
        default:
            return ErrorGuidance(title: "Sync failed")
        }
    }
}

struct SummaryError: View {
    let message: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            BannerText(message.sentenceCased)
                .textSelection(.enabled)
            Button(copied ? "Copied" : "Copy Error") {
                RepoActions.copy(message)
                copied = true
            }
            .buttonStyle(.link)
            .font(.system(size: 11))
        }
        .task(id: copied) {
            guard copied else { return }
            try? await Task.sleep(for: .seconds(2))
            copied = false
        }
    }
}

struct ErrorDetails: View {
    let message: String
    @State private var expanded = false

    private var shortMessage: String { ErrorClean.short(message) }
    private var hasDetails: Bool { message.count > shortMessage.count }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(shortMessage)
                .font(.system(size: 11, design: .monospaced))
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .lineLimit(expanded ? nil : 3)
            HStack(spacing: 12) {
                if hasDetails {
                    Button(expanded ? "Hide Details" : "Show Details") {
                        withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() }
                    }
                }
                Button("Copy Error") { RepoActions.copy(message) }
                Spacer()
            }
            .buttonStyle(.link)
            .font(.system(size: 11))
            if expanded {
                ScrollView {
                    Text(message)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(8)
                }
                .frame(maxHeight: 180)
                .background(
                    RoundedRectangle(cornerRadius: 6)
                        .fill(Color.primary.opacity(0.06))
                )
            }
        }
    }
}

struct StartDaemonButton: View {
    let appState: AppState
    var prominent = false
    @State private var starting = false

    var body: some View {
        Button {
            starting = true
            Task {
                await appState.startDaemon()
                starting = false
            }
        } label: {
            HStack(spacing: 6) {
                if starting {
                    ProgressView()
                        .controlSize(.small)
                }
                Text(starting ? "Starting…" : "Start Daemon")
            }
        }
        .disabled(starting)
        .help("Start the git-auto-sync background daemon")
        .modifier(ProminentIf(prominent: prominent))
    }
}

private struct ProminentIf: ViewModifier {
    let prominent: Bool

    func body(content: Content) -> some View {
        if prominent {
            content.buttonStyle(.borderedProminent).controlSize(.large)
        } else {
            content.controlSize(.small)
        }
    }
}

enum DaemonMismatch {
    static func appIsOlder(_ daemonProtocol: Int) -> Bool {
        daemonProtocol > DaemonCommand.protocolVersion
    }

    static func title(_ daemonProtocol: Int) -> String {
        appIsOlder(daemonProtocol) ? "App is outdated" : "Daemon is outdated"
    }

    static func message(_ daemonProtocol: Int) -> String {
        if appIsOlder(daemonProtocol) {
            return "The daemon is newer than this app. Update both from the git-auto-sync repo:"
        }
        return "The running daemon is older than this app. Update both from the git-auto-sync repo:"
    }
}

struct DaemonBanner: View {
    let appState: AppState

    var body: some View {
        switch appState.displayConnection {
        case .daemonNotRunning:
            IssueBanner(tint: .gray, symbol: "bolt.slash", title: "Daemon not running") {
                BannerText("Nothing is syncing right now. Start the daemon to pick up where it left off.")
                StartDaemonButton(appState: appState)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
        case .incompatible(let version):
            IssueBanner(tint: StatusStyle.of(.conflict).color, symbol: "arrow.up.circle", title: DaemonMismatch.title(version)) {
                BannerText(DaemonMismatch.message(version))
                CommandSnippet(command: "just install")
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 8)
        case .connected, .connecting:
            EmptyView()
        }
    }
}

struct DaemonUnavailableView: View {
    let appState: AppState

    var body: some View {
        VStack(spacing: 14) {
            Image(systemName: icon)
                .font(.system(size: 36, weight: .light))
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text(title)
                .font(.system(size: 20, weight: .semibold))
            Text(message)
                .font(.system(size: 13))
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 380)
            if case .incompatible = appState.displayConnection {
                CommandSnippet(command: "just install")
                    .frame(maxWidth: 260)
            } else {
                StartDaemonButton(appState: appState, prominent: true)
            }
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var icon: String {
        if case .incompatible = appState.displayConnection { return "arrow.up.circle" }
        return "bolt.slash"
    }

    private var title: String {
        if case .incompatible(let version) = appState.displayConnection { return DaemonMismatch.title(version) }
        return "Daemon not running"
    }

    private var message: String {
        if case .incompatible(let version) = appState.displayConnection {
            return DaemonMismatch.message(version)
        }
        return "The background daemon does the committing, pulling, and pushing. Start it to resume syncing."
    }
}

struct NoticeToast: View {
    let notice: AppNotice
    let onDismiss: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: notice.isError ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                .foregroundStyle(notice.isError ? Palette.red : Palette.green)
                .accessibilityHidden(true)
            Text(notice.text)
                .font(.system(size: 12))
                .lineLimit(4)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button(action: onDismiss) {
                Image(systemName: "xmark")
                    .font(.system(size: 10, weight: .semibold))
                    .frame(width: 16, height: 16)
            }
            .buttonStyle(.borderless)
            .help("Dismiss")
            .accessibilityLabel("Dismiss")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .frame(maxWidth: 520)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(.regularMaterial)
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder((notice.isError ? Palette.red : Color.primary).opacity(notice.isError ? 0.35 : 0.08), lineWidth: 0.7)
        )
        .shadow(color: .black.opacity(0.12), radius: 8, y: 2)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isStaticText)
        .task(id: notice.id) {
            guard !notice.isError else { return }
            try? await Task.sleep(for: .seconds(4))
            guard !Task.isCancelled else { return }
            onDismiss()
        }
    }
}
