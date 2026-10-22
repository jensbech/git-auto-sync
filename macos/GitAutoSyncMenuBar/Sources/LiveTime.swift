import SwiftUI

struct LiveRelativeText: View {
    let date: Date
    var style: RelativeDateTimeFormatter.UnitsStyle = .abbreviated
    var prefix: String = ""

    var body: some View {
        TimelineView(.periodic(from: .now, by: tickInterval)) { ctx in
            Text(prefix + format(date, now: ctx.date))
                .monospacedDigit()
        }
    }

    private var tickInterval: TimeInterval {
        let age = abs(date.timeIntervalSinceNow)
        if age < 60 { return 1 }
        if age < 3600 { return 30 }
        return 300
    }

    private func format(_ d: Date, now: Date) -> String {
        RelativeTime.format(d, relativeTo: now, style: style)
    }
}

enum RelativeTime {
    private static let abbreviated = formatter(.abbreviated)
    private static let full = formatter(.full)
    private static let short = formatter(.short)

    private static func formatter(_ style: RelativeDateTimeFormatter.UnitsStyle) -> RelativeDateTimeFormatter {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = style
        return f
    }

    static func format(_ date: Date, relativeTo now: Date = .now, style: RelativeDateTimeFormatter.UnitsStyle = .abbreviated) -> String {
        let delta = now.timeIntervalSince(date)
        if delta < 5 { return "just now" }
        let f: RelativeDateTimeFormatter
        switch style {
        case .full: f = full
        case .short: f = short
        default: f = abbreviated
        }
        return f.localizedString(for: date, relativeTo: now)
    }
}

enum ErrorClean {
    static func short(_ raw: String?) -> String {
        guard let raw, !raw.isEmpty else { return "" }
        var s = raw
        if let i = s.range(of: "\nstacktrace") { s = String(s[..<i.lowerBound]) }
        if let i = s.range(of: "\nStdErr:") { s = String(s[..<i.lowerBound]) }
        if let i = s.range(of: "\nStdOut:") { s = String(s[..<i.lowerBound]) }
        if let i = s.range(of: "\nEnv:") { s = String(s[..<i.lowerBound]) }
        if let i = s.range(of: "\nCommand:") { s = String(s[..<i.lowerBound]) }
        s = s.trimmingCharacters(in: .whitespacesAndNewlines)
        if s.count > 240 { s = String(s.prefix(240)) + "…" }
        return s
    }

    static func isSummary(_ raw: String) -> Bool {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return !trimmed.isEmpty && trimmed.count <= 240 && !trimmed.contains("\n")
    }
}
