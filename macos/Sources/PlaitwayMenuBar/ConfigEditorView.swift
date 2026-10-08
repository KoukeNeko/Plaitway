import AppKit
import PlaitwayClient
import SwiftUI

/// A plain text editor for a profile: monospaced, with line numbers, the directives and values
/// in colour, the placeholders of hidden secrets set apart, and the line the daemon refused
/// marked. It is an AppKit text view because SwiftUI's TextEditor has neither line numbers nor
/// highlighting before macOS 26.
struct ConfigEditorView: NSViewRepresentable {
    @Binding var text: String
    let kind: ProfileKind
    /// A line of `text` (1-based) to mark as wrong.
    let markedLine: Int?
    /// False while the text is being saved: what is typed then would be replaced by what comes back.
    var isEditable = true

    func makeCoordinator() -> Coordinator {
        Coordinator(self)
    }

    func makeNSView(context: Context) -> NSScrollView {
        // TextKit 1: the line numbers and the highlighting below are made for its layout manager.
        let textView = EditorTextView(usingTextLayoutManager: false)
        textView.delegate = context.coordinator
        textView.isRichText = false
        textView.allowsUndo = true
        textView.usesFindBar = true
        textView.isIncrementalSearchingEnabled = true
        textView.isAutomaticQuoteSubstitutionEnabled = false
        textView.isAutomaticDashSubstitutionEnabled = false
        textView.isAutomaticTextReplacementEnabled = false
        textView.isAutomaticSpellingCorrectionEnabled = false
        textView.isAutomaticLinkDetectionEnabled = false
        textView.isAutomaticDataDetectionEnabled = false
        textView.isContinuousSpellCheckingEnabled = false
        // Profile text holds keys while they are shown: it goes nowhere else.
        textView.writingToolsBehavior = .none
        textView.font = Coordinator.font
        textView.textColor = .labelColor
        textView.drawsBackground = false
        textView.textContainerInset = NSSize(width: 6, height: 10)
        textView.isVerticallyResizable = true
        textView.isHorizontallyResizable = false
        textView.autoresizingMask = [.width]
        textView.textContainer?.widthTracksTextView = true
        textView.setAccessibilityLabel(String(localized: "Configuration", bundle: .module))
        textView.setAccessibilityIdentifier("configuration.editor")

        let scrollView = NSScrollView()
        scrollView.hasVerticalScroller = true
        scrollView.drawsBackground = false
        // Nothing of the margin is drawn outside the editor: its separator was seen running up
        // into the bar above.
        scrollView.clipsToBounds = true
        scrollView.documentView = textView
        scrollView.hasVerticalRuler = true
        scrollView.rulersVisible = true
        let ruler = LineNumberRuler(scrollView: scrollView, textView: textView)
        ruler.clipsToBounds = true
        scrollView.verticalRulerView = ruler

        context.coordinator.textView = textView
        textView.string = text
        context.coordinator.restyle()
        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        let coordinator = context.coordinator
        coordinator.parent = self
        guard let textView = coordinator.textView else { return }
        textView.isEditable = isEditable
        if textView.string != text {
            // The model changed the text (a revert, secrets shown or hidden): the typing history is of another text.
            let selection = textView.selectedRange()
            textView.string = text
            textView.setSelectedRange(NSRange(location: min(selection.location, (text as NSString).length), length: 0))
            textView.undoManager?.removeAllActions()
        }
        coordinator.restyle()
    }

    static func dismantleNSView(_ scrollView: NSScrollView, coordinator: Coordinator) {
        // What was typed while keys were shown stays in the undo history of the view otherwise.
        (scrollView.documentView as? NSTextView)?.undoManager?.removeAllActions()
    }

    @MainActor
    final class Coordinator: NSObject, NSTextViewDelegate {
        static let font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)

        var parent: ConfigEditorView
        weak var textView: NSTextView?

        init(_ parent: ConfigEditorView) {
            self.parent = parent
        }

        func textDidChange(_ notification: Notification) {
            guard let textView else { return }
            parent.text = textView.string
            restyle()
        }

        /// Colours the text by what each part is. Temporary attributes: they are drawn, and are
        /// neither part of the text nor of its undo history.
        func restyle() {
            guard let textView, let layoutManager = textView.layoutManager else { return }
            let text = textView.string
            let whole = NSRange(location: 0, length: (text as NSString).length)
            for key in [NSAttributedString.Key.foregroundColor, .backgroundColor, .font] {
                layoutManager.removeTemporaryAttribute(key, forCharacterRange: whole)
            }

            for token in ConfigTokenizer.tokens(in: text, kind: parent.kind) {
                guard let style = Self.style(for: token.role) else { continue }
                var attributes: [NSAttributedString.Key: Any] = [.foregroundColor: style.color]
                if style.isBold { attributes[.font] = NSFont.monospacedSystemFont(ofSize: 12, weight: .semibold) }
                layoutManager.addTemporaryAttributes(attributes, forCharacterRange: NSRange(token.range, in: text))
            }
            for range in SecretMask.placeholderRanges(in: text) {
                layoutManager.addTemporaryAttributes([
                    .foregroundColor: NSColor.secondaryLabelColor,
                    .backgroundColor: NSColor.tertiaryLabelColor.withAlphaComponent(0.2),
                ], forCharacterRange: NSRange(range, in: text))
            }
            if let line = parent.markedLine, let range = Self.range(ofLine: line, in: text) {
                layoutManager.addTemporaryAttribute(.backgroundColor, value: NSColor.systemRed.withAlphaComponent(0.18), forCharacterRange: range)
            }
            if let ruler = textView.enclosingScrollView?.verticalRulerView as? LineNumberRuler {
                ruler.markedLine = parent.markedLine
                ruler.needsDisplay = true
            }
        }

        /// The line of the insertion point is marked in the text and in the margin.
        func textViewDidChangeSelection(_ notification: Notification) {
            guard let textView else { return }
            textView.needsDisplay = true
            textView.enclosingScrollView?.verticalRulerView?.needsDisplay = true
        }

        private static func style(for role: ConfigToken.Role) -> (color: NSColor, isBold: Bool)? {
            switch role {
            case .section, .blockTag: (.systemPurple, true)
            case .directive: (.systemBlue, false)
            case .number, .port, .ipAddress, .cidr: (.systemTeal, false)
            case .comment, .base64Key, .blockBody: (.secondaryLabelColor, false)
            case .argument, .hostname: nil
            }
        }

        /// The characters of a line (1-based), without its line break.
        static func range(ofLine line: Int, in text: String) -> NSRange? {
            guard line > 0 else { return nil }
            let string = text as NSString
            var number = 1
            var index = 0
            while index < string.length {
                let lineRange = string.lineRange(for: NSRange(location: index, length: 0))
                if number == line {
                    var end = NSMaxRange(lineRange)
                    while end > lineRange.location, [10, 13].contains(string.character(at: end - 1)) { end -= 1 }
                    return NSRange(location: lineRange.location, length: end - lineRange.location)
                }
                number += 1
                index = NSMaxRange(lineRange)
            }
            return nil
        }
    }
}

/// The text view of the editor. It tints the line of the insertion point, across the whole width,
/// while it has the focus; a selection is not a line to mark.
private final class EditorTextView: NSTextView {
    override func drawBackground(in rect: NSRect) {
        super.drawBackground(in: rect)
        guard window?.firstResponder === self, selectedRange().length == 0,
              let band = currentLineBand() else { return }
        NSColor.labelColor.withAlphaComponent(0.05).setFill()
        band.intersection(rect).fill()
    }

    override func becomeFirstResponder() -> Bool {
        needsDisplay = true
        enclosingScrollView?.verticalRulerView?.needsDisplay = true
        return super.becomeFirstResponder()
    }

    override func resignFirstResponder() -> Bool {
        needsDisplay = true
        enclosingScrollView?.verticalRulerView?.needsDisplay = true
        return super.resignFirstResponder()
    }

    /// The full-width strip of the line the insertion point is on, in the coordinates of the view.
    private func currentLineBand() -> NSRect? {
        guard let layoutManager, textContainer != nil else { return nil }
        let length = (string as NSString).length
        let location = selectedRange().location
        let lineRect: NSRect
        if location >= length {
            // At the end: the last line, or the empty one a final line break leaves.
            if length > 0, (string as NSString).character(at: length - 1) == 10 {
                lineRect = layoutManager.extraLineFragmentRect
            } else if length == 0 {
                lineRect = layoutManager.extraLineFragmentRect
            } else {
                lineRect = layoutManager.lineFragmentRect(forGlyphAt: layoutManager.numberOfGlyphs - 1, effectiveRange: nil)
            }
        } else {
            lineRect = layoutManager.lineFragmentRect(forGlyphAt: layoutManager.glyphIndexForCharacter(at: location), effectiveRange: nil)
        }
        guard lineRect.height > 0 else { return nil }
        return NSRect(x: 0, y: lineRect.minY + textContainerOrigin.y, width: bounds.width, height: lineRect.height)
    }
}

/// The line numbers beside the text.
private final class LineNumberRuler: NSRulerView {
    private weak var textView: NSTextView?
    private static let font = NSFont.monospacedDigitSystemFont(ofSize: 10, weight: .regular)
    private static let emphasisFont = NSFont.monospacedDigitSystemFont(ofSize: 10, weight: .semibold)
    /// The line the daemon refused (1-based): its number is red.
    var markedLine: Int?

    init(scrollView: NSScrollView, textView: NSTextView) {
        self.textView = textView
        super.init(scrollView: scrollView, orientation: .verticalRuler)
        clientView = textView
        ruleThickness = 36
        // Follows the scrolling; the text view reports its own changes through the layout.
        scrollView.contentView.postsBoundsChangedNotifications = true
        NotificationCenter.default.addObserver(self, selector: #selector(redraw), name: NSView.boundsDidChangeNotification, object: scrollView.contentView)
        NotificationCenter.default.addObserver(self, selector: #selector(redraw), name: NSText.didChangeNotification, object: textView)
    }

    required init(coder: NSCoder) {
        fatalError("init(coder:) is not used")
    }

    @objc private func redraw() {
        needsDisplay = true
    }

    /// The number (1-based) of the line a character is on: the line breaks before it, and one.
    private func lineNumber(at location: Int, in string: NSString) -> Int {
        var number = 1
        var index = 0
        while index < min(location, string.length) {
            let line = string.lineRange(for: NSRange(location: index, length: 0))
            if NSMaxRange(line) <= location { number += 1 }
            index = NSMaxRange(line)
        }
        return number
    }

    override func drawHashMarksAndLabels(in rect: NSRect) {
        guard let textView, let layoutManager = textView.layoutManager, let container = textView.textContainer else { return }
        let string = textView.string as NSString
        let visible = textView.visibleRect
        // A margin of its own, set apart from the text by a tint; its last point is the separator.
        NSColor.labelColor.withAlphaComponent(0.04).setFill()
        NSRect(x: 0, y: 0, width: bounds.width - 1, height: bounds.height).fill()
        let plain: [NSAttributedString.Key: Any] = [.font: Self.font, .foregroundColor: NSColor.tertiaryLabelColor]
        let current: [NSAttributedString.Key: Any] = [.font: Self.emphasisFont, .foregroundColor: NSColor.secondaryLabelColor]
        let refused: [NSAttributedString.Key: Any] = [.font: Self.emphasisFont, .foregroundColor: NSColor.systemRed]
        let currentLine = textView.window?.firstResponder === textView ? lineNumber(at: textView.selectedRange().location, in: string) : nil

        func draw(_ number: Int, lineRect: NSRect) {
            let attributes = number == markedLine ? refused : (number == currentLine ? current : plain)
            let label = NSAttributedString(string: "\(number)", attributes: attributes)
            let size = label.size()
            let y = lineRect.minY + textView.textContainerOrigin.y - visible.minY + (lineRect.height - size.height) / 2
            label.draw(at: NSPoint(x: ruleThickness - size.width - 6, y: y))
        }

        guard string.length > 0 else {
            draw(1, lineRect: NSRect(x: 0, y: 0, width: 0, height: 16))
            return
        }
        let glyphRange = layoutManager.glyphRange(forBoundingRect: visible, in: container)
        let firstCharacter = layoutManager.characterRange(forGlyphRange: glyphRange, actualGlyphRange: nil).location
        // The number of the first line on screen: the line breaks before it, and one.
        var number = 1
        var scan = 0
        while scan < firstCharacter {
            let line = string.lineRange(for: NSRange(location: scan, length: 0))
            if NSMaxRange(line) <= firstCharacter { number += 1 }
            scan = NSMaxRange(line)
        }
        var index = string.lineRange(for: NSRange(location: firstCharacter, length: 0)).location
        while index < string.length {
            let line = string.lineRange(for: NSRange(location: index, length: 0))
            let glyph = layoutManager.glyphIndexForCharacter(at: line.location)
            let lineRect = layoutManager.lineFragmentRect(forGlyphAt: glyph, effectiveRange: nil)
            if lineRect.minY + textView.textContainerOrigin.y > visible.maxY { return }
            draw(number, lineRect: lineRect)
            number += 1
            index = NSMaxRange(line)
        }
        // A text that ends with a line break has one more, empty, line.
        if string.character(at: string.length - 1) == 10 {
            draw(number, lineRect: layoutManager.extraLineFragmentRect)
        }
    }
}
