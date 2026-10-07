import Foundation
import PlaitwayClient

enum Formatting {
    /// Decimal units, as Finder counts them.
    static func bytes(_ count: UInt64) -> String {
        Int64(clamping: count).formatted(.byteCount(style: .file))
    }

    static func rate(_ bytesPerSecond: Double) -> String {
        let amount = Int64(bytesPerSecond.rounded()).formatted(.byteCount(style: .file))
        return String(localized: "\(amount)/s", bundle: .module)
    }

    /// "host:port (tcp)" for an endpoint of a profile.
    static func endpoint(host: String, port: UInt32, protocol transport: String) -> String {
        var text = port == 0 ? host : "\(host):\(port)"
        if !transport.isEmpty { text += " (\(transport))" }
        return text
    }

    /// A time of day that is the same width in every language, so that log lines line up.
    static func logTime(_ date: Date) -> String {
        date.formatted(Date.VerbatimFormatStyle(
            format: "\(hour: .twoDigits(clock: .twentyFourHour, hourCycle: .zeroBased)):\(minute: .twoDigits):\(second: .twoDigits)",
            locale: Locale(identifier: "en_US_POSIX"),
            timeZone: .current,
            calendar: Calendar(identifier: .gregorian)
        ))
    }
}

extension LogLine {
    var date: Date? { hasTime ? time.date : nil }
}
