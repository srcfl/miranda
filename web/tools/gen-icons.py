#!/usr/bin/env python3
"""Generate the PWA icons (stdlib only; deterministic output).

Draws the terminal-prompt glyph (">_") in the app's accent green on its dark
background and writes the PNGs referenced by manifest.json. Re-run after
changing the design:

    python3 tools/gen-icons.py
"""
import os
import struct
import zlib

BG = (0x0B, 0x0E, 0x14)  # --bg in index.html
FG = (0x4A, 0xDE, 0x80)  # --acc in index.html

# Glyph as line segments in unit coordinates: ">" chevron + "_" cursor.
SEGMENTS = [
    ((0.30, 0.335), (0.495, 0.50)),
    ((0.495, 0.50), (0.30, 0.665)),
    ((0.575, 0.655), (0.72, 0.655)),
]
HALF_WIDTH = 0.042  # stroke half-width in unit coordinates


def seg_dist(px, py, ax, ay, bx, by):
    vx, vy = bx - ax, by - ay
    wx, wy = px - ax, py - ay
    t = max(0.0, min(1.0, (wx * vx + wy * vy) / (vx * vx + vy * vy)))
    dx, dy = px - (ax + t * vx), py - (ay + t * vy)
    return (dx * dx + dy * dy) ** 0.5


def render(size, glyph_scale):
    # Rounded strokes via distance-to-segment, antialiased over ~1px.
    hw = HALF_WIDTH * glyph_scale * size
    segs = [
        tuple(((c - 0.5) * glyph_scale + 0.5) * size for c in (ax, ay, bx, by))
        for (ax, ay), (bx, by) in SEGMENTS
    ]
    rows = []
    for y in range(size):
        row = bytearray([0])  # PNG row filter: none
        py = y + 0.5
        for x in range(size):
            px = x + 0.5
            d = min(seg_dist(px, py, *s) for s in segs)
            cov = max(0.0, min(1.0, hw - d + 0.5))
            row += bytes(round(b + (f - b) * cov) for b, f in zip(BG, FG))
        rows.append(bytes(row))
    return b"".join(rows)


def chunk(tag, data):
    return (
        struct.pack(">I", len(data))
        + tag
        + data
        + struct.pack(">I", zlib.crc32(tag + data))
    )


def write_png(path, size, glyph_scale):
    ihdr = struct.pack(">IIBBBBB", size, size, 8, 2, 0, 0, 0)  # 8-bit RGB
    idat = zlib.compress(render(size, glyph_scale), 9)
    with open(path, "wb") as f:
        f.write(
            b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", ihdr)
            + chunk(b"IDAT", idat)
            + chunk(b"IEND", b"")
        )
    print(f"wrote {path} ({size}x{size})")


def main():
    out = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "icons")
    os.makedirs(out, exist_ok=True)
    write_png(os.path.join(out, "icon-192.png"), 192, 1.0)
    write_png(os.path.join(out, "icon-512.png"), 512, 1.0)
    # Maskable icons must keep the glyph inside the central 80% safe zone so
    # circular/squircle launcher masks don't clip it.
    write_png(os.path.join(out, "icon-maskable-512.png"), 512, 0.78)
    write_png(os.path.join(out, "apple-touch-icon.png"), 180, 1.0)


if __name__ == "__main__":
    main()
