// svg2png —— 托盘图标生成工具：把项目 assets 下的 SVG 源光栅化为透明 PNG。
//
// 用法（在项目根目录执行）：
//
//	swift cmd/genicons/svg2png.swift assets/tray-icon.svg assets/tray-icon.png [size=36]
//	swift cmd/genicons/svg2png.swift assets/tray-icon-failed.svg assets/tray-icon-failed.png [size=36]
//
// 实现说明：
//   - NSImage 原生支持 SVG（macOS 10.14+，CoreGraphics 解码），忠实还原
//     path/贝塞尔/fill-rule（含 evenodd 挖孔），不做任何近似重绘；
//   - 输出 RGBA PNG（透明背景 + 黑色路径），供 main 包 go:embed 后
//     经 wails SetTemplateIcon 作为 macOS template image 使用；
//   - 仅依赖 macOS 系统框架与 swift 命令（随 Xcode CLT 提供），零第三方依赖。
//   - 替代说明：曾用纯 Go 程序化绘制 F/! 图标（已删除）；本脚本从 SVG 源
//     重新生成，不会恢复旧图形。
import AppKit

let args = CommandLine.arguments
guard args.count >= 3 else {
    fputs("用法: swift svg2png.swift <input.svg> <output.png> [size=36]\n", stderr)
    exit(2)
}
let size = args.count > 3 ? Int(args[3]) ?? 36 : 36

guard let svg = NSImage(contentsOf: URL(fileURLWithPath: args[1])) else {
    fputs("无法读取 SVG（需 .svg 扩展名）: \(args[1])\n", stderr)
    exit(1)
}

guard let rep = NSBitmapImageRep(
    bitmapDataPlanes: nil,
    pixelsWide: size,
    pixelsHigh: size,
    bitsPerSample: 8,
    samplesPerPixel: 4,
    hasAlpha: true,
    isPlanar: false,
    colorSpaceName: .deviceRGB,
    bytesPerRow: 0,
    bitsPerPixel: 0
) else {
    fputs("创建位图失败\n", stderr)
    exit(1)
}
rep.size = NSSize(width: size, height: size)

NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
NSColor.clear.setFill()
NSRect(x: 0, y: 0, width: size, height: size).fill()
svg.draw(in: NSRect(x: 0, y: 0, width: size, height: size))
NSGraphicsContext.restoreGraphicsState()

guard let png = rep.representation(using: .png, properties: [:]) else {
    fputs("PNG 编码失败\n", stderr)
    exit(1)
}
do {
    try png.write(to: URL(fileURLWithPath: args[2]))
} catch {
    fputs("写出失败: \(error)\n", stderr)
    exit(1)
}
print("已生成 \(args[2]) (\(size)x\(size))")
