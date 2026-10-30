#!/usr/bin/env python3
"""Draws the GCC Code app icon and writes AppIcon.icns next to this file.

A rounded square in the brand gradient with a white cube: several accounts
in one pool. Needs Pillow and, for the .icns, macOS's iconutil.
"""
import os
import shutil
import subprocess
import sys
import tempfile

from PIL import Image, ImageChops, ImageDraw, ImageFilter

HERE = os.path.dirname(os.path.abspath(__file__))
SIZE = 1024
SCALE = 4  # draw big, then shrink for smooth edges


def lerp(a, b, t):
    return tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(3))


def gradient(w, h, c0, c1):
    """Diagonal gradient from the top-left c0 to the bottom-right c1."""
    img = Image.new("RGB", (w, h))
    px = img.load()
    for y in range(h):
        for x in range(w):
            t = (x / w + y / h) / 2
            px[x, y] = lerp(c0, c1, t)
    return img


def rounded_mask(w, h, radius):
    m = Image.new("L", (w, h), 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, w - 1, h - 1), radius=radius, fill=255)
    return m


def draw_icon():
    S = SIZE * SCALE
    canvas = Image.new("RGBA", (S, S), (0, 0, 0, 0))

    # Apple's template: the shape fills 824/1024 of the canvas.
    inset = int(100 * SCALE)
    side = S - 2 * inset
    radius = int(side * 0.225)

    # Soft drop shadow.
    shadow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shadow)
    sd.rounded_rectangle((inset, inset + 18 * SCALE, inset + side, inset + side + 18 * SCALE), radius=radius, fill=(40, 60, 140, 110))
    shadow = shadow.filter(ImageFilter.GaussianBlur(26 * SCALE))
    canvas.alpha_composite(shadow)

    # Gradient body.
    body = gradient(side, side, (0x4F, 0x7C, 0xFF), (0x8B, 0x5C, 0xF6)).convert("RGBA")
    body.putalpha(rounded_mask(side, side, radius))
    canvas.alpha_composite(body, (inset, inset))

    # Light sheen across the top.
    sheen = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    shd = ImageDraw.Draw(sheen)
    shd.ellipse((-side * 0.2, -side * 0.75, side * 1.2, side * 0.55), fill=(255, 255, 255, 46))
    sheen = sheen.filter(ImageFilter.GaussianBlur(30 * SCALE))
    sheen.putalpha(ImageChops.multiply(sheen.getchannel("A"), rounded_mask(side, side, radius)))
    canvas.alpha_composite(sheen, (inset, inset))

    # The cube.
    cx, cy = S / 2, S / 2 + 10 * SCALE
    r = side * 0.30  # distance from the centre to a top/bottom vertex
    hx = r * 0.866  # horizontal half-width
    top = (cx, cy - r)
    ul = (cx - hx, cy - r / 2)
    ur = (cx + hx, cy - r / 2)
    mid = (cx, cy)
    bl = (cx - hx, cy + r / 2)
    br = (cx + hx, cy + r / 2)
    bot = (cx, cy + r)

    cube = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    cd = ImageDraw.Draw(cube)
    cd.polygon([top, ur, mid, ul], fill=(255, 255, 255, 250))
    cd.polygon([ul, mid, bot, bl], fill=(255, 255, 255, 150))
    cd.polygon([mid, ur, br, bot], fill=(255, 255, 255, 205))
    # Edges, so the faces read as one object at small sizes.
    w = int(14 * SCALE)
    for a, b in [(top, ul), (top, ur), (ul, mid), (ur, mid), (mid, bot), (ul, bl), (ur, br), (bl, bot), (br, bot)]:
        cd.line([a, b], fill=(255, 255, 255, 255), width=w, joint="curve")
    for p in [top, ul, ur, mid, bl, br, bot]:
        cd.ellipse((p[0] - w / 2, p[1] - w / 2, p[0] + w / 2, p[1] + w / 2), fill=(255, 255, 255, 255))

    # Shadow under the cube for depth.
    cube_shadow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    ImageDraw.Draw(cube_shadow).polygon([top, ur, br, bot, bl, ul], fill=(30, 30, 90, 70))
    cube_shadow = cube_shadow.filter(ImageFilter.GaussianBlur(18 * SCALE))
    offset = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    offset.alpha_composite(cube_shadow, (0, int(22 * SCALE)))
    canvas.alpha_composite(offset)
    canvas.alpha_composite(cube)

    return canvas.resize((SIZE, SIZE), Image.LANCZOS)


def main():
    icon = draw_icon()
    png = os.path.join(HERE, "icon-1024.png")
    icon.save(png)
    if sys.platform != "darwin" or not shutil.which("iconutil"):
        print("wrote", png, "(iconutil not available, skipping .icns)")
        return
    with tempfile.TemporaryDirectory() as tmp:
        iconset = os.path.join(tmp, "AppIcon.iconset")
        os.mkdir(iconset)
        for size in (16, 32, 128, 256, 512):
            icon.resize((size, size), Image.LANCZOS).save(os.path.join(iconset, f"icon_{size}x{size}.png"))
            icon.resize((size * 2, size * 2), Image.LANCZOS).save(os.path.join(iconset, f"icon_{size}x{size}@2x.png"))
        out = os.path.join(HERE, "AppIcon.icns")
        subprocess.run(["iconutil", "-c", "icns", iconset, "-o", out], check=True)
        print("wrote", out)


if __name__ == "__main__":
    main()
