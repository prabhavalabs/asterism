#!/usr/bin/env python3
"""Generate every Asterism brand asset from one geometry definition.

Outputs:
  docs/brand/   SVG masters: mark, one-colour marks, dark/light lockups, OG source
  web/public/   favicon.svg/.ico, app icons, maskable icon, OG image, web manifest

Requirements: Python 3.10+, fontTools, Pillow, and `rsvg-convert` (librsvg).
The wordmark is converted to outlines from Manrope (SIL OFL 1.1), so no font
is needed to view the outputs. Pass the variable font file with --font:

  curl -L -o /tmp/Manrope.ttf \
    'https://github.com/google/fonts/raw/main/ofl/manrope/Manrope%5Bwght%5D.ttf'
  python3 scripts/brand/generate.py --font /tmp/Manrope.ttf
"""

from __future__ import annotations

import argparse
import io
import json
import subprocess
from pathlib import Path

from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont
from fontTools.varLib import instancer
from PIL import Image

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs" / "brand"
PUBLIC = ROOT / "web" / "public"

# --- Geometry -----------------------------------------------------------------
# Quadrant: four tiles on a 64 grid (tiles 25, gutter 4). Only the inner
# corners are rounded (r 16), so the gutters open into a four-point star.
QUAD = (
    "M15 5H27A3 3 0 0 1 30 8V14A16 16 0 0 1 14 30H8A3 3 0 0 1 5 27V15A10 10 0 0 1 15 5Z"
    "M49 5H37A3 3 0 0 0 34 8V14A16 16 0 0 0 50 30H56A3 3 0 0 0 59 27V15A10 10 0 0 0 49 5Z"
    "M15 59H27A3 3 0 0 0 30 56V50A16 16 0 0 0 14 34H8A3 3 0 0 0 5 37V49A10 10 0 0 0 15 59Z"
    "M49 59H37A3 3 0 0 1 34 56V50A16 16 0 0 1 50 34H56A3 3 0 0 1 59 37V49A10 10 0 0 1 49 59Z"
)
STOPS = (("0", "#8fb0ff"), (".34", "#4d7dff"), (".7", "#1fb6cf"), ("1", "#3fe0a4"))
INK_DARK = "#0d0f14"   # wordmark on light backgrounds
INK_LIGHT = "#f3f5fa"  # wordmark on dark backgrounds
BG_DARK = "#07080b"


def gradient(gid: str, x1=5, y1=5, x2=59, y2=59, transform: str = "") -> str:
    stops = "".join(f'<stop offset="{o}" stop-color="{c}"/>' for o, c in STOPS)
    t = f' gradientTransform="{transform}"' if transform else ""
    return (f'<linearGradient id="{gid}" gradientUnits="userSpaceOnUse" '
            f'x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}"{t}>{stops}</linearGradient>')


def mark_group(fill: str, x: float = 0, y: float = 0, size: float = 64) -> str:
    s = size / 64
    return f'<path transform="translate({x:g} {y:g}) scale({s:g})" fill="{fill}" d="{QUAD}"/>'


# --- Wordmark outlines ----------------------------------------------------------
class Wordmark:
    def __init__(self, font_path: Path, weight: int):
        font = TTFont(font_path)
        if "fvar" in font:
            font = instancer.instantiateVariableFont(font, {"wght": weight})
        self.font = font
        self.upm = font["head"].unitsPerEm
        self.cmap = font.getBestCmap()
        self.glyphs = font.getGlyphSet()
        self.hmtx = font["hmtx"]
        self.ascent = font["hhea"].ascent
        os2 = font["OS/2"]
        self.cap = getattr(os2, "sCapHeight", 0) or int(self.upm * 0.7)
        self.xh = getattr(os2, "sxHeight", 0) or int(self.upm * 0.5)

    def path(self, text: str, size: float, tracking_em: float = 0.0) -> tuple[str, float]:
        """Return (svg path d, advance width) with the baseline at y=0."""
        scale = size / self.upm
        pen = SVGPathPen(self.glyphs)
        x = 0.0
        for i, ch in enumerate(text):
            name = self.cmap[ord(ch)]
            tp = TransformPen(pen, (scale, 0, 0, -scale, x, 0))
            self.glyphs[name].draw(tp)
            adv = self.hmtx[name][0] * scale
            x += adv + (tracking_em * size if i < len(text) - 1 else 0)
        return pen.getCommands(), x


# --- SVG documents ---------------------------------------------------------------
def svg_doc(w: float, h: float, body: str, defs: str = "", title: str = "Asterism") -> str:
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{w:g}" height="{h:g}" '
            f'viewBox="0 0 {w:g} {h:g}" role="img" aria-label="{title}"><title>{title}</title>'
            f'{"<defs>" + defs + "</defs>" if defs else ""}{body}</svg>\n')


def mark_svg() -> str:
    return svg_doc(64, 64, mark_group("url(#g)"), gradient("g"))


def mono_svg(color: str) -> str:
    return svg_doc(64, 64, mark_group(color))


def lockup_svg(wm: Wordmark, ink: str) -> str:
    # Mark 64, wordmark x-height optically matched to the tile height.
    size = 52
    d, adv = wm.path("asterism", size, tracking_em=-0.035)
    gap = 16
    baseline = 32 + (wm.xh * size / wm.upm) / 2
    w = 64 + gap + adv
    body = mark_group("url(#g)") + (
        f'<path transform="translate({64 + gap} {baseline:.2f})" fill="{ink}" d="{d}"/>')
    return svg_doc(round(w + 1), 64, body, gradient("g"))


def app_icon_svg(size: int, mark_ratio: float, rounded: bool) -> str:
    m = size * mark_ratio
    off = (size - m) / 2
    rx = size * 0.225 if rounded else 0
    # The gradient is in the mark's own 64-unit space: the path's transform applies to it.
    defs = (gradient("g")
            + f'<linearGradient id="bg" x1="0" y1="0" x2="0.45" y2="1">'
              f'<stop offset="0" stop-color="#171c27"/><stop offset="1" stop-color="{BG_DARK}"/></linearGradient>')
    body = (f'<rect width="{size}" height="{size}" rx="{rx:g}" fill="url(#bg)"/>'
            f'<path transform="translate({off:g} {off:g}) scale({m / 64:g})" fill="url(#g)" d="{QUAD}"/>')
    return svg_doc(size, size, body, defs)


def og_svg(wm: Wordmark, wm_body: Wordmark) -> str:
    W, H = 1200, 630
    mark = 176
    mx, my = 104, (H - mark) / 2 - 36
    word_size = 112
    d_word, _ = wm.path("asterism", word_size, tracking_em=-0.035)
    d_tag, _ = wm_body.path("Self-hosted VPS observatory", 40, tracking_em=-0.01)
    d_sub, _ = wm_body.path("Projects, containers, ports and traffic, live on one map.", 26)
    d_url, _ = wm_body.path("github.com/prabhavalabs/asterism", 22, tracking_em=0.01)
    tx = mx + mark + 44
    word_base = my + mark / 2 + (wm.xh * word_size / wm.upm) / 2
    defs = (
        gradient("g")
        + '<radialGradient id="glow" cx="300" cy="300" r="420" gradientUnits="userSpaceOnUse">'
          '<stop offset="0" stop-color="#4d7dff" stop-opacity=".30"/>'
          '<stop offset=".55" stop-color="#1fb6cf" stop-opacity=".08"/>'
          '<stop offset="1" stop-color="#1fb6cf" stop-opacity="0"/></radialGradient>'
        + '<pattern id="dots" width="24" height="24" patternUnits="userSpaceOnUse">'
          '<circle cx="12" cy="12" r="1.2" fill="#ffffff" fill-opacity=".07"/></pattern>'
        + '<linearGradient id="fade" x1="0" y1="0" x2="1" y2="0">'
          '<stop offset="0" stop-color="#fff" stop-opacity="1"/><stop offset="1" stop-color="#fff" stop-opacity=".15"/></linearGradient>'
        + '<mask id="dm"><rect width="1200" height="630" fill="url(#fade)"/></mask>'
        + gradient("rule", x1=0, y1=0, x2=W, y2=0)
    )
    body = (
        f'<rect width="{W}" height="{H}" fill="{BG_DARK}"/>'
        f'<rect width="{W}" height="{H}" fill="url(#dots)" mask="url(#dm)"/>'
        f'<rect width="{W}" height="{H}" fill="url(#glow)"/>'
        f'<path transform="translate({mx:g} {my:g}) scale({mark / 64:g})" fill="url(#g)" d="{QUAD}"/>'
        f'<path transform="translate({tx:g} {word_base:.1f})" fill="{INK_LIGHT}" d="{d_word}"/>'
        f'<path transform="translate({mx:g} {my + mark + 92:g})" fill="#c9d1e3" d="{d_tag}"/>'
        f'<path transform="translate({mx:g} {my + mark + 138:g})" fill="#8792ab" d="{d_sub}"/>'
        f'<path transform="translate({mx:g} {H - 56:g})" fill="#6f7a94" d="{d_url}"/>'
        f'<rect x="0" y="{H - 6}" width="{W}" height="6" fill="url(#rule)"/>'
    )
    return svg_doc(W, H, body, defs, title="Asterism, self-hosted VPS observatory")


# --- Rasterising ------------------------------------------------------------------
def rasterize(svg: str, width: int, height: int | None = None) -> Image.Image:
    args = ["rsvg-convert", "-w", str(width)]
    if height:
        args += ["-h", str(height)]
    png = subprocess.run(args, input=svg.encode(), capture_output=True, check=True).stdout
    return Image.open(io.BytesIO(png)).convert("RGBA")


def save_png(img: Image.Image, path: Path, opaque: bool = False) -> None:
    if opaque:
        bg = Image.new("RGB", img.size, BG_DARK)
        bg.paste(img, mask=img.split()[3])
        img = bg
    img.save(path, optimize=True)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--font", type=Path, required=True, help="Manrope variable TTF")
    args = ap.parse_args()

    bold = Wordmark(args.font, 700)
    medium = Wordmark(args.font, 500)
    DOCS.mkdir(parents=True, exist_ok=True)
    PUBLIC.mkdir(parents=True, exist_ok=True)

    masters = {
        "mark.svg": mark_svg(),
        "mark-white.svg": mono_svg("#ffffff"),
        "mark-black.svg": mono_svg("#000000"),
        "logo-dark.svg": lockup_svg(bold, INK_LIGHT),   # for dark backgrounds
        "logo-light.svg": lockup_svg(bold, INK_DARK),   # for light backgrounds
        "og-image.svg": og_svg(bold, medium),
    }
    for name, svg in masters.items():
        (DOCS / name).write_text(svg)

    # Favicon: the colour mark reads on both light and dark browser chrome.
    (PUBLIC / "favicon.svg").write_text(mark_svg())
    ico_sizes = [16, 32, 48]
    ico_imgs = [rasterize(mark_svg(), s) for s in ico_sizes]
    ico_imgs[-1].save(PUBLIC / "favicon.ico", sizes=[(s, s) for s in ico_sizes], append_images=ico_imgs[:-1])

    # Home-screen icons sit on the dark tile. Apple and maskable icons are full
    # bleed (the platform applies its own mask); maskable keeps the 80% safe zone.
    save_png(rasterize(app_icon_svg(180, 0.6, rounded=False), 180), PUBLIC / "apple-touch-icon.png", opaque=True)
    for s in (192, 512):
        save_png(rasterize(app_icon_svg(s, 0.62, rounded=True), s), PUBLIC / f"icon-{s}.png")
    save_png(rasterize(app_icon_svg(512, 0.5, rounded=False), 512), PUBLIC / "icon-maskable-512.png", opaque=True)

    save_png(rasterize(masters["og-image.svg"], 1200, 630), PUBLIC / "og-image.png", opaque=True)
    (PUBLIC / "logo-dark.svg").write_text(masters["logo-dark.svg"])
    (PUBLIC / "logo-light.svg").write_text(masters["logo-light.svg"])

    manifest = {
        "name": "Asterism",
        "short_name": "Asterism",
        "description": "Self-hosted VPS observatory: projects, containers, ports and traffic, live on one map.",
        "start_url": "/",
        "scope": "/",
        "display": "standalone",
        "background_color": BG_DARK,
        "theme_color": BG_DARK,
        "icons": [
            {"src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
            {"src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
            {"src": "/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
            {"src": "/favicon.svg", "sizes": "any", "type": "image/svg+xml"},
        ],
    }
    (PUBLIC / "site.webmanifest").write_text(json.dumps(manifest, indent=2) + "\n")
    print("brand assets written to", DOCS.relative_to(ROOT), "and", PUBLIC.relative_to(ROOT))


if __name__ == "__main__":
    main()
