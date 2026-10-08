import AppKit
import PlaitwayClient
import SwiftUI

/// How much of a log to show: debug lines are most of what openvpn writes and hide the rest.
enum LogFilter: CaseIterable, Hashable {
    case all, info, warnings, errors

    var title: Text {
        switch self {
        case .all: Text("All levels", bundle: .module)
        case .info: Text("Info and above", bundle: .module)
        case .warnings: Text("Warnings and errors", bundle: .module)
        case .errors: Text("Errors only", bundle: .module)
        }
    }

    func includes(_ level: LogLevel) -> Bool {
        switch self {
        case .all: true
        case .info: level != .debug
        case .warnings: level == .warn || level == .error
        case .errors: level == .error
        }
    }
}

/// A live log: a profile's, or the daemon's own for an empty id. It follows the newest line
/// while it is scrolled to the end and stays where it is once the person scrolls up to read.
struct LogView: View {
    let profileID: String
    @Environment(AppModel.self) private var model
    @State private var tail = LogTail()
    @State private var filter: LogFilter = .info
    @State private var query = ""
    @State private var position = ScrollPosition(edge: .bottom)
    @State private var isAtEnd = true
    @FocusState private var isSearching: Bool

    var body: some View {
        let lines = visibleLines
        VStack(spacing: 0) {
            PageBar {
                TextField(text: $query, prompt: Text("Search Logs", bundle: .module)) {
                    Text("Search Logs", bundle: .module)
                }
                .textFieldStyle(.roundedBorder)
                .focused($isSearching)
                .onExitCommand { query = "" }
                .overlay(alignment: .trailing) {
                    if !query.isEmpty {
                        Button { query = "" } label: {
                            Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
                        }
                        .buttonStyle(.borderless)
                        .padding(.trailing, 6)
                        .accessibilityLabel(Text("Clear", bundle: .module))
                    }
                }
                .accessibilityIdentifier("logs.search")
                Picker(selection: $filter) {
                    ForEach(LogFilter.allCases, id: \.self) { filter in filter.title.tag(filter) }
                } label: {
                    Text("Level", bundle: .module)
                }
                .labelsHidden()
                .pickerStyle(.menu)
                .fixedSize()
                .accessibilityIdentifier("logs.level")
                Button { position.scrollTo(edge: .bottom) } label: {
                    Label { Text("Latest", bundle: .module) } icon: { Image(systemName: "arrow.down.to.line") }
                        .labelStyle(.iconOnly)
                }
                .help(Text("Latest", bundle: .module))
                .disabled(isAtEnd)
                .accessibilityIdentifier("logs.latest")
                Button { copy(lines) } label: {
                    Label { Text("Copy", bundle: .module) } icon: { Image(systemName: "doc.on.doc") }
                        .labelStyle(.iconOnly)
                }
                .help(Text("Copy", bundle: .module))
                .disabled(lines.isEmpty)
                .accessibilityIdentifier("logs.copy")
            }
            Group {
                if lines.isEmpty {
                    if query.isEmpty {
                        ContentUnavailableView {
                            Label { Text("No Log Lines", bundle: .module) } icon: { Image(systemName: "text.alignleft") }
                        }
                    } else {
                        ContentUnavailableView.search(text: query)
                    }
                } else {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 2) {
                            ForEach(lines) { entry in
                                LogRow(entry: entry)
                            }
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .scrollPosition($position)
                    .onScrollGeometryChange(for: Bool.self) { geometry in
                        geometry.contentOffset.y + geometry.containerSize.height >= geometry.contentSize.height - 8
                    } action: { _, atEnd in
                        isAtEnd = atEnd
                    }
                    .onChange(of: lines.last?.id) { _, _ in
                        if isAtEnd { position.scrollTo(edge: .bottom) }
                    }
                }
            }
        }
        .onChange(of: model.searchRequest) { isSearching = true }
        .task(id: profileID) { await tail.run(store: model.store, profileID: profileID) }
    }

    private var visibleLines: [LogTail.Entry] {
        tail.entries.filter { entry in
            filter.includes(entry.level) && (query.isEmpty || entry.text.localizedCaseInsensitiveContains(query))
        }
    }

    private func copy(_ lines: [LogTail.Entry]) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(lines.map(\.plainText).joined(separator: "\n"), forType: .string)
    }
}

private struct LogRow: View {
    let entry: LogTail.Entry

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(verbatim: entry.date.map(Formatting.logTime) ?? "")
                .foregroundStyle(.secondary)
                .frame(width: 62, alignment: .leading)
            Text(verbatim: entry.level.tag)
                .foregroundStyle(entry.level.tint)
                .frame(width: 44, alignment: .leading)
            Text(verbatim: entry.text)
                .foregroundStyle(entry.level == .error ? Color.red : Color.primary)
        }
        .font(.system(.caption, design: .monospaced))
        .textSelection(.enabled)
    }
}
