import Foundation
import Testing
@testable import PlaitwayMenuBar

/// The String Catalog is edited by hand (SwiftPM does not extract strings), so
/// these tests keep it in step with the sources and with the owner's rules for
/// Traditional Chinese and for UI text.
struct LocalizationTests {
    private static let packageRoot = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    private static let sourcesDirectory = packageRoot.appendingPathComponent("Sources/PlaitwayMenuBar")

    private struct Catalog: Decodable {
        struct Entry: Decodable {
            struct Localization: Decodable {
                struct Unit: Decodable { let state: String; let value: String }
                let stringUnit: Unit
            }
            let localizations: [String: Localization]?
        }
        let sourceLanguage: String
        let strings: [String: Entry]
    }

    private static func loadCatalog() throws -> Catalog {
        let url = sourcesDirectory.appendingPathComponent("Resources/Localizable.xcstrings")
        return try JSONDecoder().decode(Catalog.self, from: Data(contentsOf: url))
    }

    private static func swiftSources() throws -> [(name: String, text: String)] {
        try FileManager.default.contentsOfDirectory(at: sourcesDirectory, includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "swift" }
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
            .map { ($0.lastPathComponent, try String(contentsOf: $0, encoding: .utf8)) }
    }

    /// Format arguments are all `%@` here: the catalog says `%lld` for an Int and `%@` for a String.
    private static func normalised(_ key: String) -> String {
        var key = key
        for specifier in ["%lld", "%d"] { key = key.replacingOccurrences(of: specifier, with: "%@") }
        return key
    }

    private static func matches(_ pattern: String, in text: String) -> [[String]] {
        let regex = try! NSRegularExpression(pattern: pattern)
        return regex.matches(in: text, range: NSRange(text.startIndex..., in: text)).map { match in
            (1..<match.numberOfRanges).map { String(text[Range(match.range(at: $0), in: text)!]) }
        }
    }

    /// The literal of a Swift string with its interpolations replaced by `%@`.
    private static func key(fromLiteral literal: String) -> String {
        let interpolation = try! NSRegularExpression(pattern: #"\\\((?:[^()]|\([^()]*\))*\)"#)
        return interpolation.stringByReplacingMatches(in: literal, range: NSRange(literal.startIndex..., in: literal), withTemplate: "%@")
    }

    private static let literal = #""((?:[^"\\]|\\.)*)""#

    /// Every UI string the sources look up, with the file that does it.
    private static func usedKeys() throws -> [String: String] {
        var used: [String: String] = [:]
        for (name, text) in try swiftSources() {
            let patterns = [
                #"String\(localized:\s*"# + literal + #"\s*,\s*bundle:\s*\.module"#,
                #"Text\("# + literal + #"\s*,\s*bundle:\s*\.module"#,
                #"InfoRow\("# + literal,
            ]
            for pattern in patterns {
                for groups in matches(pattern, in: text) { used[key(fromLiteral: groups[0])] = name }
            }
            // Text(condition ? "A" : "B", bundle: .module)
            for groups in matches(#"\?\s*"# + literal + #"\s*:\s*"# + literal + #"\s*,\s*bundle:\s*\.module"#, in: text) {
                used[key(fromLiteral: groups[0])] = name
                used[key(fromLiteral: groups[1])] = name
            }
        }
        return used
    }

    @Test func everyStringTheSourcesUseIsInTheCatalog() throws {
        let catalog = Set(try Self.loadCatalog().strings.keys.map(Self.normalised))
        let used = try Self.usedKeys()
        #expect(used.count > 100, "the scan found too little: \(used.count)")
        let missing = used.filter { !catalog.contains(Self.normalised($0.key)) }
        #expect(missing.isEmpty, "not in the catalog: \(missing.map { "\($0.key) (\($0.value))" }.sorted())")
    }

    @Test func theCatalogHoldsNoStringNobodyUses() throws {
        let used = Set(try Self.usedKeys().keys.map(Self.normalised))
        let unused = try Self.loadCatalog().strings.keys.filter { !used.contains(Self.normalised($0)) }
        #expect(unused.isEmpty, "unused: \(unused.sorted())")
    }

    @Test func noStringBypassesTheModuleBundle() throws {
        // A literal that is looked up in the main bundle finds no translation in the app.
        for (name, text) in try Self.swiftSources() {
            let unbundledText = Self.matches(#"Text\("# + Self.literal + #"\s*\)"#, in: text)
            #expect(unbundledText.isEmpty, "\(name): Text without bundle: \(unbundledText)")
            let unbundledString = Self.matches(#"String\(localized:\s*"# + Self.literal + #"\s*\)"#, in: text)
            #expect(unbundledString.isEmpty, "\(name): String(localized:) without bundle: \(unbundledString)")
            for groups in Self.matches(#"\b(?:Button|Label|Toggle|Picker|Section|TextField|SecureField|LabeledContent)\("# + Self.literal, in: text) {
                Issue.record("\(name): a control takes a literal title, which is looked up in the main bundle: \(groups[0])")
            }
        }
    }

    @Test func everyStringIsTranslatedIntoTraditionalChinese() throws {
        let catalog = try Self.loadCatalog()
        #expect(catalog.sourceLanguage == "en")
        for (key, entry) in catalog.strings {
            let unit = entry.localizations?["zh-Hant"]?.stringUnit
            #expect(unit?.state == "translated", "\(key)")
            let value = try #require(unit?.value, "\(key) has no zh-Hant text")
            #expect(!value.isEmpty, "\(key)")
            let placeholders = { (text: String) in Self.normalised(text).components(separatedBy: "%@").count }
            #expect(placeholders(key) == placeholders(value), "\(key) -> \(value): the placeholders differ")
        }
    }

    @Test func theTranslationsUseTaiwanTerms() throws {
        // Mainland terms and their Taiwan replacements: 匹配 -> 配對/符合, 當前 -> 目前, 組件 -> 元件, 緩存 -> 快取, ...
        let forbidden = ["匹配", "當前", "組件", "緩存", "軟件", "默認", "設置", "用戶", "網絡", "信息", "服務器", "登錄", "程序", "文件", "數據", "鏈接", "賬", "視頻", "支持"]
        for (key, entry) in try Self.loadCatalog().strings {
            let value = entry.localizations?["zh-Hant"]?.stringUnit.value ?? ""
            for term in forbidden {
                #expect(!value.contains(term), "\(key) -> \(value) uses \(term)")
            }
        }
    }

    @Test func theTextCarriesNoPersonality() throws {
        // Routine states, errors and dialogs: no exclamation marks, no "we" or "your", no replies as buttons.
        let bannedEnglish = [" we ", "we ", "your ", "!", "please", "oops", "sorry", "successfully"]
        let bannedChinese = ["我們", "你的", "您的", "！", "請稍候", "好的", "是的", "抱歉", "成功"]
        for (key, entry) in try Self.loadCatalog().strings {
            let english = key.lowercased()
            for word in bannedEnglish { #expect(!english.contains(word), "\(key) contains \"\(word)\"") }
            let chinese = entry.localizations?["zh-Hant"]?.stringUnit.value ?? ""
            for word in bannedChinese {
                // "請在「登入項目」中允許" is the one required instruction; a plain 請 is fine.
                #expect(!chinese.contains(word), "\(key) -> \(chinese) contains \(word)")
            }
        }
    }

    @Test func theCompiledBundleHasTheTraditionalChineseStrings() throws {
        #expect(Bundle.module.developmentLocalization == "en")
        let path = try #require(Bundle.module.path(forResource: "zh-Hant", ofType: "lproj"), "the resource bundle has no zh-Hant localization")
        let chinese = try #require(Bundle(path: path))
        for (key, entry) in try Self.loadCatalog().strings {
            let expected = try #require(entry.localizations?["zh-Hant"]?.stringUnit.value)
            #expect(chinese.localizedString(forKey: key, value: "missing", table: nil) == expected, "\(key)")
        }
        #expect(chinese.localizedString(forKey: "Connected", value: nil, table: nil) == "已連線")
        #expect(String(format: chinese.localizedString(forKey: "Shadowed by %@", value: nil, table: nil), "Office") == "被 Office 遮蔽")
    }

    @Test func sameActionSameLabel() throws {
        // The action labels the owner's rule names: one wording everywhere an action appears.
        let used = try Self.usedKeys()
        for label in ["Import Profile…", "Delete Profile…", "Connect", "Disconnect", "Retry", "Reinstall Helper", "Cancel", "Remove"] {
            #expect(used[label] != nil, "\(label) is not used")
        }
        let importLabels = used.keys.filter { $0.lowercased().contains("import") && $0.contains("…") }
        #expect(importLabels == ["Import Profile…"], "\(importLabels)")
        let deleteLabels = used.keys.filter { $0.hasPrefix("Delete") && $0.hasSuffix("…") }
        #expect(deleteLabels == ["Delete Profile…"], "\(deleteLabels)")
    }
}
