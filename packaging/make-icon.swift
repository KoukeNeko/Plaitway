// Draws the Plaitway app icon, a shield with a small network graph, into a
// .iconset directory. package-app.sh turns it into Plaitway.icns with iconutil.
//
//	swift packaging/make-icon.swift build/Plaitway.iconset
//
// Only CoreGraphics and ImageIO: no assets, no AppKit, works headless.
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

// Each size is drawn at its own pixel size rather than scaled down, so small
// sizes keep crisp edges.
let iconsetEntries: [(name: String, pixels: Int)] = [
    ("icon_16x16", 16), ("icon_16x16@2x", 32),
    ("icon_32x32", 32), ("icon_32x32@2x", 64),
    ("icon_128x128", 128), ("icon_128x128@2x", 256),
    ("icon_256x256", 256), ("icon_256x256@2x", 512),
    ("icon_512x512", 512), ("icon_512x512@2x", 1024),
]

let colorSpace = CGColorSpace(name: CGColorSpace.sRGB)!

func rgb(_ hex: UInt32, alpha: CGFloat = 1) -> CGColor {
    CGColor(
        colorSpace: colorSpace,
        components: [
            CGFloat((hex >> 16) & 0xFF) / 255, CGFloat((hex >> 8) & 0xFF) / 255,
            CGFloat(hex & 0xFF) / 255, alpha,
        ])!
}

/// The macOS icon silhouette is a superellipse, not a plain rounded rectangle.
func squirclePath(in rect: CGRect, exponent: CGFloat = 5) -> CGPath {
    let path = CGMutablePath()
    let steps = 360
    for step in 0...steps {
        let angle = CGFloat(step) / CGFloat(steps) * 2 * .pi
        let c = cos(angle), s = sin(angle)
        let x = rect.midX + rect.width / 2 * (c < 0 ? -1 : 1) * pow(abs(c), 2 / exponent)
        let y = rect.midY + rect.height / 2 * (s < 0 ? -1 : 1) * pow(abs(s), 2 / exponent)
        step == 0 ? path.move(to: CGPoint(x: x, y: y)) : path.addLine(to: CGPoint(x: x, y: y))
    }
    path.closeSubpath()
    return path
}

/// A shield spanning x in -0.78...0.78 and y in -1...0.96 around the origin.
func shieldPath() -> CGPath {
    let path = CGMutablePath()
    path.move(to: CGPoint(x: 0, y: 0.96))
    path.addCurve(to: CGPoint(x: -0.78, y: 0.72), control1: CGPoint(x: -0.28, y: 0.86), control2: CGPoint(x: -0.55, y: 0.78))
    path.addLine(to: CGPoint(x: -0.78, y: 0.12))
    path.addCurve(to: CGPoint(x: 0, y: -1.0), control1: CGPoint(x: -0.78, y: -0.45), control2: CGPoint(x: -0.4, y: -0.8))
    path.addCurve(to: CGPoint(x: 0.78, y: 0.12), control1: CGPoint(x: 0.4, y: -0.8), control2: CGPoint(x: 0.78, y: -0.45))
    path.addLine(to: CGPoint(x: 0.78, y: 0.72))
    path.addCurve(to: CGPoint(x: 0, y: 0.96), control1: CGPoint(x: 0.55, y: 0.78), control2: CGPoint(x: 0.28, y: 0.86))
    path.closeSubpath()
    return path
}

func gradient(_ top: UInt32, _ bottom: UInt32) -> CGGradient {
    CGGradient(colorsSpace: colorSpace, colors: [rgb(top), rgb(bottom)] as CFArray, locations: [0, 1])!
}

func drawIcon(in context: CGContext, pixels: Int) {
    let size = CGFloat(pixels)
    // The icon grid leaves a margin around the artwork.
    let artwork = CGRect(x: 0, y: 0, width: size, height: size).insetBy(dx: size * 0.098, dy: size * 0.098)

    context.saveGState()
    context.setShadow(offset: CGSize(width: 0, height: -size * 0.01), blur: size * 0.025, color: rgb(0x000000, alpha: 0.35))
    context.addPath(squirclePath(in: artwork))
    context.setFillColor(rgb(0x0B3A8F))
    context.fillPath()
    context.restoreGState()

    context.saveGState()
    context.addPath(squirclePath(in: artwork))
    context.clip()
    context.drawLinearGradient(
        gradient(0x3FA9FF, 0x0B3A8F),
        start: CGPoint(x: artwork.midX, y: artwork.maxY), end: CGPoint(x: artwork.midX, y: artwork.minY), options: [])
    context.restoreGState()

    // Everything below is drawn in shield units: the origin is the artwork
    // centre and one unit is 0.36 of the artwork width.
    context.saveGState()
    let unit = artwork.width * 0.36
    context.translateBy(x: artwork.midX, y: artwork.midY - unit * 0.02)
    context.scaleBy(x: unit, y: unit)

    context.saveGState()
    context.setShadow(offset: CGSize(width: 0, height: -0.04), blur: 0.08, color: rgb(0x00122E, alpha: 0.45))
    context.addPath(shieldPath())
    context.setFillColor(rgb(0xFFFFFF))
    context.fillPath()
    context.restoreGState()

    context.saveGState()
    context.addPath(shieldPath())
    context.clip()
    context.drawLinearGradient(
        gradient(0xFFFFFF, 0xCFE4FF),
        start: CGPoint(x: 0, y: 0.96), end: CGPoint(x: 0, y: -1), options: [])
    context.restoreGState()

    // The network: a hub linked to three nodes, which are linked to each other.
    let ink = rgb(0x0B3A8F)
    let hub = CGPoint(x: 0, y: 0.12)
    let nodes = [90.0, 210.0, 330.0].map { degrees -> CGPoint in
        let radians = CGFloat(degrees) * .pi / 180
        return CGPoint(x: hub.x + 0.52 * cos(radians), y: hub.y + 0.52 * sin(radians))
    }
    context.setStrokeColor(ink)
    context.setLineWidth(0.06)
    context.setLineCap(.round)
    context.setLineJoin(.round)
    for node in nodes {
        context.move(to: hub)
        context.addLine(to: node)
    }
    context.strokePath()
    context.setStrokeColor(rgb(0x0B3A8F, alpha: 0.45))
    context.setLineWidth(0.04)
    context.addLines(between: nodes + [nodes[0]])
    context.strokePath()

    context.setFillColor(ink)
    for node in nodes {
        context.fillEllipse(in: CGRect(x: node.x - 0.115, y: node.y - 0.115, width: 0.23, height: 0.23))
    }
    context.setFillColor(rgb(0x3FA9FF))
    context.fillEllipse(in: CGRect(x: hub.x - 0.2, y: hub.y - 0.2, width: 0.4, height: 0.4))
    context.setStrokeColor(ink)
    context.setLineWidth(0.05)
    context.strokeEllipse(in: CGRect(x: hub.x - 0.2, y: hub.y - 0.2, width: 0.4, height: 0.4))
    context.restoreGState()
}

func writePNG(pixels: Int, to url: URL) throws {
    guard
        let context = CGContext(
            data: nil, width: pixels, height: pixels, bitsPerComponent: 8, bytesPerRow: 0,
            space: colorSpace, bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue),
        let image = { () -> CGImage? in
            drawIcon(in: context, pixels: pixels)
            return context.makeImage()
        }(),
        let destination = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)
    else {
        throw CocoaError(.fileWriteUnknown)
    }
    CGImageDestinationAddImage(destination, image, nil)
    guard CGImageDestinationFinalize(destination) else { throw CocoaError(.fileWriteUnknown) }
}

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: make-icon.swift OUTPUT.iconset\n".utf8))
    exit(2)
}
let outputDirectory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
do {
    try FileManager.default.createDirectory(at: outputDirectory, withIntermediateDirectories: true)
    for entry in iconsetEntries {
        try writePNG(pixels: entry.pixels, to: outputDirectory.appendingPathComponent(entry.name + ".png"))
    }
} catch {
    FileHandle.standardError.write(Data("make-icon: \(error)\n".utf8))
    exit(1)
}
