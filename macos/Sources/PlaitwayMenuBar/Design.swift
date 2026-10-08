import AppKit
import PlaitwayClient
import SwiftUI

// How the app shows a state. Every state has a shape of its own, a word and a colour, in that
// order of importance: the colour alone fails people who cannot tell green from red, and
// yellow and green are below 3:1 against a white window.

extension ProfileState {
    /// The shield of the menu bar item, in the state of the profile: the same family in the
    /// sidebar, on the page and in the menu bar.
    var symbolName: String {
        switch self {
        case .connected: "lock.shield.fill"
        case .connecting: "lock.rotation"
        case .reconnecting: "arrow.triangle.2.circlepath"
        case .disconnecting: "lock.open"
        case .awaitingCredentials: "key.fill"
        case .failed: "exclamationmark.shield.fill"
        case .disconnected, .unspecified, .UNRECOGNIZED: "lock.shield"
        }
    }

    var tint: Color {
        switch self {
        case .connected: .green
        case .connecting, .reconnecting, .awaitingCredentials, .disconnecting: .orange
        case .failed: .red
        case .disconnected, .unspecified, .UNRECOGNIZED: .secondary
        }
    }

    /// Something is going on that ends by itself or needs the user: the glyph may move.
    var isTransitional: Bool {
        switch self {
        case .connecting, .reconnecting, .disconnecting: true
        default: false
        }
    }
}

extension RouteState {
    var symbolName: String {
        switch self {
        case .installed: "checkmark.circle.fill"
        case .pending: "clock"
        case .shadowed: "minus.circle"
        case .blocked: "exclamationmark.triangle.fill"
        case .failed: "xmark.octagon.fill"
        case .unspecified, .UNRECOGNIZED: "circle"
        }
    }

    /// A shadowed prefix is a profile standing by behind a higher priority one, which is how
    /// it is meant to work: neutral. A blocked one is a conflict the user may want to know about.
    var tint: Color {
        switch self {
        case .installed: .green
        case .pending, .blocked: .orange
        case .failed: .red
        case .shadowed, .unspecified, .UNRECOGNIZED: .secondary
        }
    }

    /// The prefix is not in the routing table although the profile asks for it.
    var isLost: Bool {
        switch self {
        case .blocked, .failed: true
        default: false
        }
    }
}

/// A state's glyph. It moves while the state is transitional, unless the user asked for less motion.
struct StatusGlyph: View {
    let symbol: String
    let tint: Color
    var isActive = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(_ state: ProfileState) {
        symbol = state.symbolName
        tint = state.tint
        isActive = state.isTransitional
    }

    init(_ state: RouteState) {
        symbol = state.symbolName
        tint = state.tint
    }

    var body: some View {
        Image(systemName: symbol)
            .foregroundStyle(tint)
            .symbolEffect(.pulse, isActive: isActive && !reduceMotion)
            .accessibilityHidden(true)
    }
}

/// A glyph and the word for the state, which is what assistive technology reads.
struct StatusLabel: View {
    let glyph: StatusGlyph
    let text: String

    init(_ state: ProfileState) {
        glyph = StatusGlyph(state)
        text = state.label
    }

    init(_ state: RouteState, text: String? = nil) {
        glyph = StatusGlyph(state)
        self.text = text ?? state.label()
    }

    var body: some View {
        HStack(spacing: 5) {
            glyph
            Text(verbatim: text)
        }
    }
}

/// The pages of a profile, or of Diagnostics: one of them is shown at a time.
protocol PageSet: Hashable, CaseIterable {
    var label: String { get }
}

/// The page switcher at the right of the toolbar, as the view switcher of a Finder window is: the
/// pages of the profile that is selected, or of Diagnostics, and nothing for an empty window.
///
/// It is AppKit's control and follows the model itself. A toolbar item whose content is chosen by
/// SwiftUI is replaced by the toolbar when that content changes, and the toolbar is then seen to
/// be built again with every page that is chosen; this item never changes.
struct SectionPicker: NSViewRepresentable {
    let model: AppModel
    let identifier: String

    func makeCoordinator() -> Coordinator { Coordinator(model: model) }

    func makeNSView(context: Context) -> NSSegmentedControl {
        let control = NSSegmentedControl(labels: [], trackingMode: .selectOne, target: context.coordinator, action: #selector(Coordinator.changed))
        control.segmentDistribution = .fit
        control.setAccessibilityIdentifier(identifier)
        context.coordinator.control = control
        context.coordinator.follow()
        return control
    }

    func updateNSView(_ control: NSSegmentedControl, context: Context) {}

    @MainActor
    final class Coordinator: NSObject {
        let model: AppModel
        weak var control: NSSegmentedControl?

        init(model: AppModel) { self.model = model }

        @objc func changed(_ sender: NSSegmentedControl) {
            let index = sender.selectedSegment
            switch model.selection {
            case .profile:
                if let section = Array(ProfileSection.allCases)[safe: index] { model.profileSection = section }
            case .diagnostics:
                if let page = Array(DiagnosticsPage.allCases)[safe: index] { model.diagnosticsPage = page }
            case nil:
                break
            }
        }

        /// Shows the pages and the one that is open, and again each time the model changes them.
        func follow() {
            guard let control else { return }
            let labels: [String]
            let current: Int?
            if model.selectedProfile != nil {
                labels = ProfileSection.allCases.map(\.label)
                current = Array(ProfileSection.allCases).firstIndex(of: model.profileSection)
            } else if model.selection == .diagnostics {
                labels = DiagnosticsPage.allCases.map(\.label)
                current = Array(DiagnosticsPage.allCases).firstIndex(of: model.diagnosticsPage)
            } else {
                labels = []
                current = nil
            }
            if control.segmentCount != labels.count {
                control.segmentCount = labels.count
            }
            for (index, label) in labels.enumerated() where control.label(forSegment: index) != label {
                control.setLabel(label, forSegment: index)
            }
            control.isHidden = labels.isEmpty
            control.selectedSegment = current ?? -1
            withObservationTracking {
                _ = model.selection
                _ = model.selectedProfile != nil
                _ = model.profileSection
                _ = model.diagnosticsPage
            } onChange: { [weak self] in
                Task { @MainActor in self?.follow() }
            }
        }
    }
}

private extension Array {
    subscript(safe index: Int) -> Element? {
        indices.contains(index) ? self[index] : nil
    }
}

/// What a page can do, in a strip at its top. These are not toolbar items: a window toolbar that
/// gains and loses items as the page changes is built again as a whole, the buttons that stay in
/// it included.
struct PageBar<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) { content }
                .buttonStyle(.bordered)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
            Divider()
        }
    }
}

struct ToolbarGap: ToolbarContent {
    var body: some ToolbarContent {
        if #available(macOS 26, *) {
            ToolbarSpacer(.fixed, placement: .primaryAction)
        }
    }
}
