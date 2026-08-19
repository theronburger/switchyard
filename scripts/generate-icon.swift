#!/usr/bin/env swift

import AppKit
import Foundation

guard CommandLine.arguments.count == 4 else {
    FileHandle.standardError.write(Data("usage: generate-icon.swift TILE_PNG DOCK_PNG MENU_PNG\n".utf8))
    exit(2)
}

guard let tile = NSImage(contentsOfFile: CommandLine.arguments[1]) else {
    FileHandle.standardError.write(Data("could not load tile image\n".utf8))
    exit(2)
}

func point(_ x: CGFloat, _ y: CGFloat, scale: CGFloat) -> NSPoint {
    NSPoint(x: x * scale, y: y * scale)
}

func drawBackground(size: CGFloat) {
    let bounds = NSRect(x: 0, y: 0, width: size, height: size)
    tile.draw(in: bounds, from: .zero, operation: .sourceOver, fraction: 1)
}

func drawMark(size: CGFloat, color: NSColor) {
    let scale = size / 1024
    let nodeRadius = 92 * scale
    let nodeStrokeWidth = 34 * scale
    let trackWidth = 38 * scale
    let lowerLeft = point(342, 342, scale: scale)
    let upperRight = point(682, 682, scale: scale)
    let routeLength = hypot(upperRight.x - lowerLeft.x, upperRight.y - lowerLeft.y)
    let routeInsetX = (upperRight.x - lowerLeft.x) / routeLength * nodeRadius
    let routeInsetY = (upperRight.y - lowerLeft.y) / routeLength * nodeRadius

    let route = NSBezierPath()
    route.move(to: NSPoint(x: lowerLeft.x + routeInsetX, y: lowerLeft.y + routeInsetY))
    route.line(to: NSPoint(x: upperRight.x - routeInsetX, y: upperRight.y - routeInsetY))
    let shadow = NSShadow()
    shadow.shadowColor = NSColor.black.withAlphaComponent(0.82)
    shadow.shadowBlurRadius = 18 * scale
    shadow.shadowOffset = NSSize(width: 7 * scale, height: -10 * scale)
    shadow.set()
    NSColor.black.withAlphaComponent(0.82).setStroke()
    route.lineWidth = trackWidth + 16 * scale
    route.lineCapStyle = .round
    route.stroke()

    shadow.shadowColor = .clear
    shadow.set()
    NSColor(calibratedWhite: 0.34, alpha: 1).setStroke()
    route.lineWidth = trackWidth + 6 * scale
    route.stroke()
    color.setStroke()
    route.lineWidth = trackWidth
    route.stroke()

    let routeHighlight = NSBezierPath()
    routeHighlight.move(to: NSPoint(
        x: lowerLeft.x + routeInsetX - 3 * scale,
        y: lowerLeft.y + routeInsetY + 3 * scale
    ))
    routeHighlight.line(to: NSPoint(
        x: upperRight.x - routeInsetX - 3 * scale,
        y: upperRight.y - routeInsetY + 3 * scale
    ))
    NSColor.white.withAlphaComponent(0.30).setStroke()
    routeHighlight.lineWidth = 7 * scale
    routeHighlight.lineCapStyle = .round
    routeHighlight.stroke()

    let nodeCenters = [
        point(342, 342, scale: scale),
        point(682, 342, scale: scale),
        point(342, 682, scale: scale),
        point(682, 682, scale: scale),
    ]
    for center in nodeCenters {
        let node = NSBezierPath()
        node.appendOval(in: NSRect(
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

        shadow.shadowColor = NSColor.black.withAlphaComponent(0.82)
        shadow.shadowBlurRadius = 18 * scale
        shadow.shadowOffset = NSSize(width: 7 * scale, height: -10 * scale)
        shadow.set()
        NSColor(calibratedWhite: 0.22, alpha: 1).setFill()
        node.fill()

        shadow.shadowColor = .clear
        shadow.set()
        NSGradient(colors: [
            NSColor(calibratedWhite: 0.92, alpha: 1),
            color,
            NSColor(calibratedWhite: 0.42, alpha: 1),
        ])?.draw(in: node, angle: -55)

        NSColor.white.withAlphaComponent(0.30).setStroke()
        let highlight = NSBezierPath(ovalIn: NSRect(
            x: center.x - nodeRadius + 3 * scale,
            y: center.y - nodeRadius + 3 * scale,
            width: nodeRadius * 2 - 6 * scale,
            height: nodeRadius * 2 - 6 * scale
        ))
        highlight.lineWidth = 5 * scale
        highlight.stroke()
    }
}

func render(size: Int, hasBackground: Bool, mark: NSColor) throws -> Data {
    guard let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: size,
        pixelsHigh: size,
        bitsPerSample: 8,
        samplesPerPixel: 4,
        hasAlpha: true,
        isPlanar: false,
        colorSpaceName: .deviceRGB,
        bitmapFormat: [],
        bytesPerRow: 0,
        bitsPerPixel: 0
    ) else {
        throw CocoaError(.fileWriteUnknown)
    }
    NSGraphicsContext.saveGraphicsState()
    defer { NSGraphicsContext.restoreGraphicsState() }
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
    NSGraphicsContext.current?.imageInterpolation = .high
    let rect = NSRect(x: 0, y: 0, width: size, height: size)
    NSColor.clear.setFill()
    rect.fill()
    if hasBackground {
        drawBackground(size: CGFloat(size))
    }
    drawMark(size: CGFloat(size), color: mark)
    guard let data = bitmap.representation(using: .png, properties: [:]) else {
        throw CocoaError(.fileWriteUnknown)
    }
    return data
}

try render(
    size: 1024,
    hasBackground: true,
    mark: NSColor(calibratedWhite: 0.72, alpha: 1)
)
    .write(to: URL(fileURLWithPath: CommandLine.arguments[2]), options: .atomic)
try render(size: 128, hasBackground: false, mark: .black)
    .write(to: URL(fileURLWithPath: CommandLine.arguments[3]), options: .atomic)
