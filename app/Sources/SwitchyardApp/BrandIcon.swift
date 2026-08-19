import AppKit
import SwiftUI

enum SwitchyardBrandMarkState: Hashable {
    case brand
    case idle
    case running
}

enum SwitchyardBrandIcon {
    static let brand = render(state: .brand)
    static let idle = render(state: .idle)
    static let running = render(state: .running)

    static func image(for state: SwitchyardBrandMarkState) -> NSImage {
        switch state {
        case .brand: brand
        case .idle: idle
        case .running: running
        }
    }

    private static func render(state: SwitchyardBrandMarkState) -> NSImage {
        let pixelSize = 128
        let side = CGFloat(pixelSize)
        let bitmap = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: pixelSize,
            pixelsHigh: pixelSize,
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bitmapFormat: [],
            bytesPerRow: 0,
            bitsPerPixel: 0
        )!

        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
        NSGraphicsContext.current?.shouldAntialias = true
        NSColor.clear.setFill()
        NSRect(x: 0, y: 0, width: side, height: side).fill()
        NSColor.black.setStroke()
        NSColor.black.setFill()

        let nodeRadius = side * 0.14
        let strokeWidth = side * 0.072
        let near = side * 0.25
        let far = side * 0.75
        let centers = [
            NSPoint(x: near, y: near),
            NSPoint(x: far, y: near),
            NSPoint(x: near, y: far),
            NSPoint(x: far, y: far),
        ]

        if state != .idle {
            let lowerLeft = centers[0]
            let upperRight = centers[3]
            let routeInset = nodeRadius / sqrt(2)
            let route = NSBezierPath()
            route.move(to: NSPoint(
                x: lowerLeft.x + routeInset,
                y: lowerLeft.y + routeInset
            ))
            route.line(to: NSPoint(
                x: upperRight.x - routeInset,
                y: upperRight.y - routeInset
            ))
            route.lineWidth = strokeWidth
            route.lineCapStyle = .round
            route.stroke()
        }

        for (index, center) in centers.enumerated() {
            let node = NSBezierPath(ovalIn: NSRect(
                x: center.x - nodeRadius,
                y: center.y - nodeRadius,
                width: nodeRadius * 2,
                height: nodeRadius * 2
            ))
            if state == .running, (index == 0 || index == 3) {
                node.fill()
            } else {
                node.lineWidth = strokeWidth
                node.stroke()
            }
        }

        NSGraphicsContext.restoreGraphicsState()

        let image = NSImage(size: NSSize(width: 18, height: 18))
        image.addRepresentation(bitmap)
        image.isTemplate = true
        return image
    }
}

enum SwitchyardDockIcon {
    static let brand = render(state: .brand)
    static let idle = render(state: .idle)
    static let running = render(state: .running)

    private static let tile = Bundle.module.url(
        forResource: "SwitchyardTile",
        withExtension: "png"
    ).flatMap(NSImage.init(contentsOf:))

    static func image(for state: SwitchyardBrandMarkState) -> NSImage {
        switch state {
        case .brand: brand
        case .idle: idle
        case .running: running
        }
    }

    @MainActor
    static func apply(state: SwitchyardBrandMarkState) {
        NSApplication.shared.applicationIconImage = image(for: state)
    }

    private static func render(state: SwitchyardBrandMarkState) -> NSImage {
        let pixelSize = 1024
        let side = CGFloat(pixelSize)
        let bitmap = NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: pixelSize,
            pixelsHigh: pixelSize,
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bitmapFormat: [],
            bytesPerRow: 0,
            bitsPerPixel: 0
        )!

        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
        NSGraphicsContext.current?.shouldAntialias = true
        let bounds = NSRect(x: 0, y: 0, width: side, height: side)
        NSColor.clear.setFill()
        bounds.fill()
        if let tile {
            tile.draw(in: bounds, from: .zero, operation: .sourceOver, fraction: 1)
        } else {
            drawFallbackTile(in: bounds)
        }

        let nodeRadius: CGFloat = 92
        let nodeStrokeWidth: CGFloat = 34
        let routeWidth: CGFloat = 38
        func tilePoint(_ x: CGFloat, _ y: CGFloat) -> NSPoint { NSPoint(x: x, y: y) }
        let centers = [
            tilePoint(342, 342),
            tilePoint(682, 342),
            tilePoint(342, 682),
            tilePoint(682, 682),
        ]
        let markColor = NSColor(calibratedWhite: 0.72, alpha: 1)
        let markShadow = NSShadow()
        markShadow.shadowColor = NSColor.black.withAlphaComponent(0.82)
        markShadow.shadowBlurRadius = 18
        markShadow.shadowOffset = NSSize(width: 7, height: -10)

        if state != .idle {
            let lowerLeft = centers[0]
            let upperRight = centers[3]
            let routeInset = nodeRadius / sqrt(2)
            let route = NSBezierPath()
            route.move(to: NSPoint(
                x: lowerLeft.x + routeInset,
                y: lowerLeft.y + routeInset
            ))
            route.line(to: NSPoint(
                x: upperRight.x - routeInset,
                y: upperRight.y - routeInset
            ))
            route.lineCapStyle = .round
            markShadow.set()
            NSColor.black.withAlphaComponent(0.82).setStroke()
            route.lineWidth = routeWidth + 16
            route.stroke()

            clearShadow()
            NSColor(calibratedWhite: 0.34, alpha: 1).setStroke()
            route.lineWidth = routeWidth + 6
            route.stroke()
            markColor.setStroke()
            route.lineWidth = routeWidth
            route.stroke()

            let highlight = NSBezierPath()
            highlight.move(to: NSPoint(
                x: lowerLeft.x + routeInset - 3,
                y: lowerLeft.y + routeInset + 3
            ))
            highlight.line(to: NSPoint(
                x: upperRight.x - routeInset - 3,
                y: upperRight.y - routeInset + 3
            ))
            NSColor.white.withAlphaComponent(0.30).setStroke()
            highlight.lineWidth = 7
            highlight.lineCapStyle = .round
            highlight.stroke()
        }

        for (index, center) in centers.enumerated() {
            let connectedAndFilled = state == .running && (index == 0 || index == 3)
            if connectedAndFilled {
                let node = NSBezierPath(ovalIn: NSRect(
                    x: center.x - nodeRadius,
                    y: center.y - nodeRadius,
                    width: nodeRadius * 2,
                    height: nodeRadius * 2
                ))
                markShadow.set()
                NSColor.black.withAlphaComponent(0.82).setFill()
                node.fill()
                clearShadow()
                metallicGradient(base: markColor).draw(in: node, angle: -55)
                NSColor.white.withAlphaComponent(0.32).setStroke()
                node.lineWidth = 5
                node.stroke()
                continue
            }

            let node = NSBezierPath(ovalIn: NSRect(
                x: center.x - nodeRadius,
                y: center.y - nodeRadius,
                width: nodeRadius * 2,
                height: nodeRadius * 2
            ))
            let innerRadius = nodeRadius - nodeStrokeWidth
            node.appendOval(in: NSRect(
                x: center.x - innerRadius,
                y: center.y - innerRadius,
                width: innerRadius * 2,
                height: innerRadius * 2
            ))
            node.windingRule = .evenOdd

            markShadow.set()
            NSColor.black.withAlphaComponent(0.82).setFill()
            node.fill()
            clearShadow()
            metallicGradient(base: markColor).draw(in: node, angle: -55)

            NSColor.white.withAlphaComponent(0.30).setStroke()
            let highlight = NSBezierPath(ovalIn: NSRect(
                x: center.x - nodeRadius + 3,
                y: center.y - nodeRadius + 3,
                width: nodeRadius * 2 - 6,
                height: nodeRadius * 2 - 6
            ))
            highlight.lineWidth = 5
            highlight.stroke()
        }

        NSGraphicsContext.restoreGraphicsState()

        let image = NSImage(size: NSSize(width: side, height: side))
        image.addRepresentation(bitmap)
        image.isTemplate = false
        return image
    }

    private static func metallicGradient(base: NSColor) -> NSGradient {
        NSGradient(colors: [
            NSColor(calibratedWhite: 0.92, alpha: 1),
            base,
            NSColor(calibratedWhite: 0.42, alpha: 1),
        ])!
    }

    private static func clearShadow() {
        let shadow = NSShadow()
        shadow.shadowColor = .clear
        shadow.set()
    }

    private static func drawFallbackTile(in bounds: NSRect) {
        let tileBounds = bounds.insetBy(dx: 96, dy: 96)
        squirclePath(in: tileBounds).addClip()
        NSGradient(colors: [
            NSColor(calibratedWhite: 0.28, alpha: 1),
            NSColor(calibratedWhite: 0.10, alpha: 1),
            NSColor(calibratedWhite: 0.025, alpha: 1),
        ])?.draw(in: tileBounds, angle: -45)
    }

    private static func squirclePath(in bounds: NSRect) -> NSBezierPath {
        let path = NSBezierPath()
        let center = NSPoint(x: bounds.midX, y: bounds.midY)
        let horizontalRadius = bounds.width / 2
        let verticalRadius = bounds.height / 2
        let exponent: CGFloat = 5
        let pointCount = 256

        for index in 0...pointCount {
            let angle = CGFloat(index) / CGFloat(pointCount) * 2 * .pi
            let cosine = cos(angle)
            let sine = sin(angle)
            let point = NSPoint(
                x: center.x + horizontalRadius * copySign(pow(abs(cosine), 2 / exponent), cosine),
                y: center.y + verticalRadius * copySign(pow(abs(sine), 2 / exponent), sine)
            )
            if index == 0 {
                path.move(to: point)
            } else {
                path.line(to: point)
            }
        }
        path.close()
        return path
    }

    private static func copySign(_ magnitude: CGFloat, _ sign: CGFloat) -> CGFloat {
        sign < 0 ? -magnitude : magnitude
    }
}

struct SwitchyardBrandMark: View {
    var state = SwitchyardBrandMarkState.brand

    var body: some View {
        Image(nsImage: SwitchyardBrandIcon.image(for: state))
            .resizable()
            .scaledToFit()
            .id(state)
            .accessibilityLabel(accessibilityLabel)
    }

    private var accessibilityLabel: String {
        switch state {
        case .brand:
            "Switchyard"
        case .idle:
            "Switchyard, no environments running"
        case .running:
            "Switchyard, environments running"
        }
    }
}
