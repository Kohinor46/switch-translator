// Рисует иконку программы: Icon.png 1024×1024 (из неё fyne package делает .icns и .ico).
// Запуск: swift tools/make-icon.swift Icon.png
import AppKit

let out = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "Icon.png"
let px = 1024
let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                           samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                           colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
rep.size = NSSize(width: 1024, height: 1024)
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)

// плашка по сетке иконок macOS: 824×824 с отступом 100
let tile = CGRect(x: 100, y: 100, width: 824, height: 824)
let tilePath = NSBezierPath(roundedRect: tile, xRadius: 185, yRadius: 185)
NSGraphicsContext.saveGraphicsState()
let shadow = NSShadow()
shadow.shadowColor = NSColor.black.withAlphaComponent(0.3)
shadow.shadowBlurRadius = 28
shadow.shadowOffset = NSSize(width: 0, height: -12)
shadow.set()
NSColor.white.setFill()
tilePath.fill()
NSGraphicsContext.restoreGraphicsState()
NSGradient(starting: NSColor(srgbRed: 0.20, green: 0.25, blue: 0.78, alpha: 1),
           ending: NSColor(srgbRed: 0.10, green: 0.75, blue: 0.80, alpha: 1))!.draw(in: tilePath, angle: 60)
NSGraphicsContext.saveGraphicsState()
tilePath.addClip()
NSGradient(starting: NSColor.white.withAlphaComponent(0.20), ending: NSColor.white.withAlphaComponent(0))!
    .draw(in: CGRect(x: 100, y: 512, width: 824, height: 412), angle: -90)
NSGraphicsContext.restoreGraphicsState()

// реплика: скруглённый прямоугольник с хвостиком
func bubble(_ r: CGRect, tailLeft: Bool, fill: NSColor) {
    let p = NSBezierPath(roundedRect: r, xRadius: 70, yRadius: 70)
    let t = NSBezierPath()
    if tailLeft {
        t.move(to: CGPoint(x: r.minX + 70, y: r.minY + 10))
        t.line(to: CGPoint(x: r.minX + 30, y: r.minY - 70))
        t.line(to: CGPoint(x: r.minX + 150, y: r.minY + 10))
    } else {
        t.move(to: CGPoint(x: r.maxX - 70, y: r.minY + 10))
        t.line(to: CGPoint(x: r.maxX - 30, y: r.minY - 70))
        t.line(to: CGPoint(x: r.maxX - 150, y: r.minY + 10))
    }
    t.close()
    NSGraphicsContext.saveGraphicsState()
    let s = NSShadow()
    s.shadowColor = NSColor.black.withAlphaComponent(0.25)
    s.shadowBlurRadius = 18
    s.shadowOffset = NSSize(width: 0, height: -8)
    s.set()
    fill.setFill()
    p.fill()
    t.fill()
    NSGraphicsContext.restoreGraphicsState()
}

func letter(_ s: String, center: CGPoint, color: NSColor, size: CGFloat) {
    let attrs: [NSAttributedString.Key: Any] = [
        .font: NSFont.systemFont(ofSize: size, weight: .heavy),
        .foregroundColor: color,
    ]
    let str = NSAttributedString(string: s, attributes: attrs)
    let b = str.size()
    str.draw(at: CGPoint(x: center.x - b.width / 2, y: center.y - b.height / 2))
}

let back = CGRect(x: 180, y: 470, width: 390, height: 330)
bubble(back, tailLeft: true, fill: NSColor.white.withAlphaComponent(0.92))
letter("A", center: CGPoint(x: back.midX, y: back.midY), color: NSColor(srgbRed: 0.20, green: 0.25, blue: 0.78, alpha: 1), size: 250)

let front = CGRect(x: 450, y: 250, width: 390, height: 330)
bubble(front, tailLeft: false, fill: NSColor(srgbRed: 1.0, green: 0.80, blue: 0.20, alpha: 1))
letter("Я", center: CGPoint(x: front.midX, y: front.midY), color: NSColor(srgbRed: 0.35, green: 0.20, blue: 0.05, alpha: 1), size: 250)

NSGraphicsContext.restoreGraphicsState()
try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: out))
print("Иконка: \(out)")
