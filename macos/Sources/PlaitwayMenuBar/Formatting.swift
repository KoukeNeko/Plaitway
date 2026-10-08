import Foundation
import PlaitwayClient

enum Formatting {
    /// Decimal units, as Finder counts them. Nothing is written as a digit: the system spells it out
    /// as a word in English ("Zero kB"), which is no way to read a rate that is mostly nothing.
    static func bytes(_ count: UInt64, locale: Locale = .current) -> String {
        amount(Int64(clamping: count), locale: locale)
    }

    static func rate(_ bytesPerSecond: Double, locale: Locale = .current) -> String {
        let text = amount(Int64(bytesPerSecond.rounded()), locale: locale)
        return String(localized: "\(text)/s", bundle: .module)
    }

    private static func amount(_ count: Int64, locale: Locale) -> String {
        count.formatted(.byteCount(style: .file, spellsOutZero: false).locale(locale))
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
