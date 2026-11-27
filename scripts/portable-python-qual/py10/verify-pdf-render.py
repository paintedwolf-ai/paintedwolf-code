#!/usr/bin/env python3
"""Independent host PDF renderer and placement/clipping verifier for PY10.

Renders the generated 2-page fpdf2 PDF on the host using an independent OS/system
PDF renderer:
  - Linux: Poppler `pdftoppm -png -r 150`
  - macOS: Apple native PDFKit via `swift` (150 DPI RGBA PNG)

Then parses the rendered PNGs using pure Python stdlib (`zlib` + `struct`) to
verify:
  1. Exact rendered page count == 2 (`page-1.png`, `page-2.png`).
  2. Both pages contain visible non-white ink content (non-blank).
  3. Outer 12-pixel margin border on all 4 edges of each page has ZERO ink
     pixels (no edge text/table/image clipping).
"""

import argparse
import hashlib
import json
import platform
import shutil
import struct
import subprocess
import tempfile
import zlib
from pathlib import Path

SWIFT_PDFKIT_RENDER = r"""
import AppKit
import Foundation
import PDFKit

let pdfPath = CommandLine.arguments[1]
let outPrefix = CommandLine.arguments[2]
guard let doc = PDFDocument(url: URL(fileURLWithPath: pdfPath)) else {
    fputs("error: PDFKit failed to open \(pdfPath)\n", stderr)
    exit(1)
}
let dpi: CGFloat = 150.0
for idx in 0..<doc.pageCount {
    guard let page = doc.page(at: idx) else { continue }
    let box = page.bounds(for: .mediaBox)
    let w = Int(ceil(box.width * dpi / 72.0))
    let h = Int(ceil(box.height * dpi / 72.0))
    guard let rep = NSBitmapImageRep(
        bitmapDataPlanes: nil,
        pixelsWide: w,
        pixelsHigh: h,
        bitsPerSample: 8,
        samplesPerPixel: 4,
        hasAlpha: true,
        isPlanar: false,
        colorSpaceName: .deviceRGB,
        bytesPerRow: w * 4,
        bitsPerPixel: 32
    ) else {
        fputs("error: failed to allocate bitmap rep\n", stderr)
        exit(1)
    }
    NSGraphicsContext.saveGraphicsState()
    guard let ctx = NSGraphicsContext(bitmapImageRep: rep) else { exit(1) }
    NSGraphicsContext.current = ctx
    let cg = ctx.cgContext
    cg.setFillColor(red: 1, green: 1, blue: 1, alpha: 1)
    cg.fill(CGRect(x: 0, y: 0, width: w, height: h))
    cg.scaleBy(x: dpi / 72.0, y: dpi / 72.0)
    page.draw(with: .mediaBox, to: cg)
    NSGraphicsContext.restoreGraphicsState()
    guard let png = rep.representation(using: .png, properties: [:]) else { exit(1) }
    let outFile = "\(outPrefix)-\(idx + 1).png"
    try png.write(to: URL(fileURLWithPath: outFile))
}
print("PDFKIT_RENDERED_PAGES=\(doc.pageCount)")
"""


def sha256_file(p: Path) -> str:
    return hashlib.sha256(p.read_bytes()).hexdigest()


def decode_png_rgb(png_path: Path):
    """Pure-stdlib PNG decoder for 8-bit RGB (color_type=2) or RGBA (color_type=6)."""
    data = png_path.read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError(f"{png_path} is not a valid PNG")
    pos = 8
    width = height = bit_depth = color_type = 0
    idat = bytearray()
    while pos < len(data):
        length = struct.unpack(">I", data[pos : pos + 4])[0]
        ctype = data[pos + 4 : pos + 8]
        chunk = data[pos + 8 : pos + 8 + length]
        pos += 12 + length
        if ctype == b"IHDR":
            width, height, bit_depth, color_type, comp, filt, interlace = struct.unpack(">IIBBBBB", chunk)
            if bit_depth != 8 or color_type not in (2, 6) or interlace != 0:
                raise ValueError(f"unsupported PNG format in {png_path}: depth={bit_depth} type={color_type} interlace={interlace}")
        elif ctype == b"IDAT":
            idat.extend(chunk)
        elif ctype == b"IEND":
            break

    bpp = 3 if color_type == 2 else 4
    stride = width * bpp
    raw = zlib.decompress(bytes(idat))
    rows = []
    prev = bytearray(stride)
    idx = 0
    for _ in range(height):
        ftype = raw[idx]
        idx += 1
        cur = bytearray(raw[idx : idx + stride])
        idx += stride
        if ftype == 1:  # Sub
            for i in range(bpp, stride):
                cur[i] = (cur[i] + cur[i - bpp]) & 0xFF
        elif ftype == 2:  # Up
            for i in range(stride):
                cur[i] = (cur[i] + prev[i]) & 0xFF
        elif ftype == 3:  # Average
            for i in range(stride):
                a = cur[i - bpp] if i >= bpp else 0
                b = prev[i]
                cur[i] = (cur[i] + ((a + b) >> 1)) & 0xFF
        elif ftype == 4:  # Paeth
            for i in range(stride):
                a = cur[i - bpp] if i >= bpp else 0
                b = prev[i]
                c = prev[i - bpp] if i >= bpp else 0
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pr = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                cur[i] = (cur[i] + pr) & 0xFF
        elif ftype != 0:
            raise ValueError(f"unknown PNG filter {ftype}")
        rows.append(cur)
        prev = cur
    return width, height, bpp, rows


def inspect_rendered_page(png_path: Path, border_px: int = 12) -> dict:
    w, h, bpp, rows = decode_png_rgb(png_path)
    ink_total = 0
    edge_clip_pixels = {"top": 0, "bottom": 0, "left": 0, "right": 0}
    for y in range(h):
        row = rows[y]
        for x in range(w):
            off = x * bpp
            r, g, b = row[off], row[off + 1], row[off + 2]
            if r < 245 or g < 245 or b < 245:
                ink_total += 1
                if y < border_px:
                    edge_clip_pixels["top"] += 1
                if y >= h - border_px:
                    edge_clip_pixels["bottom"] += 1
                if x < border_px:
                    edge_clip_pixels["left"] += 1
                if x >= w - border_px:
                    edge_clip_pixels["right"] += 1
    total_px = w * h
    return {
        "file": png_path.name,
        "sha256": sha256_file(png_path),
        "width": w,
        "height": h,
        "ink_pixels": ink_total,
        "ink_ratio": round(ink_total / max(total_px, 1), 6),
        "non_blank": ink_total >= 500,
        "border_px_checked": border_px,
        "edge_clip_pixels": edge_clip_pixels,
        "zero_edge_clipping": sum(edge_clip_pixels.values()) == 0,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pdf", required=True, help="Path to generated PDF report")
    ap.add_argument("--out-dir", required=True, help="Directory to write rendered page PNGs and verdict")
    ap.add_argument("--expected-pages", type=int, default=2)
    args = ap.parse_args()

    pdf_path = Path(args.pdf).resolve()
    out_dir = Path(args.out_dir).resolve()
    out_dir.mkdir(parents=True, exist_ok=True)
    if not pdf_path.is_file():
        raise SystemExit(f"error: missing PDF {pdf_path}")

    prefix = out_dir / "pdf-page"
    system = platform.system()
    renderer = ""
    if system == "Darwin":
        renderer = "macOS PDFKit (Apple native via swift, 150 DPI)"
        with tempfile.TemporaryDirectory(prefix="py10-pdfkit-") as td:
            sw = Path(td) / "render.swift"
            sw.write_text(SWIFT_PDFKIT_RENDER, encoding="utf-8")
            subprocess.run(["swift", str(sw), str(pdf_path), str(prefix)], check=True)
    else:
        if not shutil.which("pdftoppm"):
            raise SystemExit("error: pdftoppm not found on Linux host")
        renderer = "Poppler pdftoppm (-png -r 150)"
        subprocess.run(["pdftoppm", "-png", "-r", "150", str(pdf_path), str(prefix)], check=True)

    pages = sorted(out_dir.glob("pdf-page-*.png"))
    if len(pages) != args.expected_pages:
        raise SystemExit(f"FAIL: expected {args.expected_pages} rendered pages, got {len(pages)}: {[p.name for p in pages]}")

    page_reports = [inspect_rendered_page(p) for p in pages]
    all_ok = all(r["non_blank"] and r["zero_edge_clipping"] for r in page_reports)
    verdict = {
        "status": "PASS" if all_ok else "FAIL",
        "pdf_path": str(pdf_path),
        "pdf_sha256": sha256_file(pdf_path),
        "renderer": renderer,
        "page_count": len(page_reports),
        "pages": page_reports,
    }
    verdict_path = out_dir / "pdf-render-verdict.json"
    verdict_path.write_text(json.dumps(verdict, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"{verdict['status']}: rendered {len(page_reports)} pages via {renderer} -> {verdict_path}")
    if not all_ok:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
