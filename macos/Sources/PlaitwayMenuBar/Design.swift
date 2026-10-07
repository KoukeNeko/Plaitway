import PlaitwayClient
import SwiftUI

// How the app shows a state. Every state has a shape of its own, a word and a colour, in that
// order of importance: the colour alone fails people who cannot tell green from red, and
// yellow and green are below 3:1 against a white window.

extension ProfileState {
    var symbolName: String {
        switch self {
        case .connected: "checkmark.circle.fill"
        case .connecting: "circle.dotted"
        case .reconnecting: "arrow.triangle.2.circlepath"
        case .disconnecting: "circle.dashed"
        case .awaitingCredentials: "key.fill"
        case .failed: "xmark.octagon.fill"
        case .disconnected, .unspecified, .UNRECOGNIZED: "circle"
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

/// The state of a profile in a list: a rounded square in the state's colour with a glyph of its
/// own inside. A sidebar paints the symbols it is given in its own tint, so the colour is a
/// shape here, as in the icons of System Settings.
struct StatusBadge: View {
    let state: ProfileState
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        RoundedRectangle(cornerRadius: 6, style: .continuous)
            .fill(state == .disconnected ? Color.secondary.opacity(0.45) : state.tint)
            .frame(width: 22, height: 22)
            .overlay {
                Image(systemName: state.badgeSymbolName)
                    .font(.system(size: 11, weight: .bold))
                    .foregroundStyle(.white)
                    .symbolEffect(.pulse, isActive: state.isTransitional && !reduceMotion)
            }
            .accessibilityHidden(true)
    }
}

extension ProfileState {
    /// The glyph inside a `StatusBadge`; the square around it is already the shape.
    var badgeSymbolName: String {
        switch self {
        case .connected: "checkmark"
        case .connecting, .disconnecting: "ellipsis"
        case .reconnecting: "arrow.triangle.2.circlepath"
        case .awaitingCredentials: "key.fill"
        case .failed: "exclamationmark"
        case .disconnected, .unspecified, .UNRECOGNIZED: "power"
        }
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

// macOS 26 and later draw the window's bars in glass; the app still runs on 15, where the same
// code has to give a plain bar.

extension View {
    /// The section switcher of a page: a tab control on macOS 27, a segmented one before.
    @ViewBuilder
    func sectionPickerStyle() -> some View {
        if #available(macOS 27, *) {
            pickerStyle(.tabs)
        } else {
            pickerStyle(.segmented)
        }
    }
}

/// The switcher between the pages of a profile or of Diagnostics, at the top of the page. It
/// is not in the toolbar: five names do not fit beside the title and the actions of a window
/// that is only 860 points wide, and the toolbar would move it into its overflow menu.
struct SectionPicker<Section: Hashable & CaseIterable>: View where Section.AllCases: RandomAccessCollection {
    @Binding var selection: Section
    let label: (Section) -> String

    var body: some View {
        Picker(selection: $selection) {
            ForEach(Array(Section.allCases), id: \.self) { section in
                Text(verbatim: label(section)).tag(section)
            }
        } label: {
            Text("Section", bundle: .module)
        }
        .labelsHidden()
        .sectionPickerStyle()
        .frame(maxWidth: 560)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
    }
}

struct ToolbarGap: ToolbarContent {
    var body: some ToolbarContent {
        if #available(macOS 26, *) {
            ToolbarSpacer(.fixed, placement: .primaryAction)
        }
    }
}
