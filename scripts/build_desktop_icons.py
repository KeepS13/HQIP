"""Regenerate the desktop SVG, PNG and multi-size ICO assets (requires Pillow)."""
from pathlib import Path
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[1]
SCALE = 8
STATES = {
    'disconnected': ('#aaa2ef', '#6254ac', '#716887', 'minus'),
    'connecting': ('#91b9ff', '#4b63d3', '#517bea', 'dots'),
    'connected': ('#79e3c5', '#20876f', '#20a17d', 'check'),
    'recovering': ('#ffdb91', '#b77c30', '#cf8b24', 'pause'),
    'failed': ('#ffa6b1', '#bd4866', '#d44660', 'alert'),
}


def render_icon(top, bottom, badge, glyph):
    size = 64 * SCALE
    image = Image.new('RGBA', (size, size))
    mask = Image.new('L', (size, size))
    ImageDraw.Draw(mask).rounded_rectangle((4*SCALE, 4*SCALE, 60*SCALE, 60*SCALE), 17*SCALE, fill=255)
    start = tuple(bytes.fromhex(top[1:]))
    end = tuple(bytes.fromhex(bottom[1:]))
    draw = ImageDraw.Draw(image)
    for y in range(size):
        ratio = max(0, min(1, (y/SCALE-4)/56))
        color = tuple(round(a+(b-a)*ratio) for a, b in zip(start, end))
        draw.line((0, y, size, y), fill=(*color, 255))
    image.putalpha(mask)
    draw = ImageDraw.Draw(image)
    svg = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><defs><linearGradient id="tile" x2="0" y2="1"><stop stop-color="{top}"/><stop offset="1" stop-color="{bottom}"/></linearGradient></defs><rect x="4" y="4" width="56" height="56" rx="17" fill="url(#tile)"/>']

    def line(points, color, width):
        scaled = [(round(x*SCALE), round(y*SCALE)) for x, y in points]
        draw.line(scaled, fill=color, width=round(width*SCALE), joint='curve')
        radius = width*SCALE/2
        for x, y in scaled:
            draw.ellipse((x-radius, y-radius, x+radius, y+radius), fill=color)
        svg.append(f'<polyline points="{" ".join(f"{x},{y}" for x,y in points)}" fill="none" stroke="{color}" stroke-width="{width}" stroke-linecap="round" stroke-linejoin="round"/>')

    def circle(x, y, radius, color):
        draw.ellipse(((x-radius)*SCALE, (y-radius)*SCALE, (x+radius)*SCALE, (y+radius)*SCALE), fill=color)
        svg.append(f'<circle cx="{x}" cy="{y}" r="{radius}" fill="{color}"/>')

    # A broad, rounded H stays legible at 16px and carries the existing brand.
    for points in [[(20,19),(20,43)],[(40,19),(40,40)],[(20,31),(40,31)]]:
        line(points, '#ffffff', 6)
    circle(49, 49, 13, '#111827')
    circle(49, 49, 10.5, badge)
    if glyph == 'minus':
        line([(44,49),(54,49)], '#ffffff', 2.5)
    elif glyph == 'dots':
        for x in [44,49,54]:
            circle(x,49,1.5,'#ffffff')
    elif glyph == 'check':
        line([(43.5,49),(47.5,53),(55,45.5)], '#ffffff', 2.6)
    elif glyph == 'pause':
        line([(46,45),(46,53)], '#ffffff', 2.6)
        line([(52,45),(52,53)], '#ffffff', 2.6)
    elif glyph == 'alert':
        line([(49,43.5),(49,49.5)], '#ffffff', 2.8)
        circle(49,54,1.5,'#ffffff')
    svg.append('</svg>')
    return image, ''.join(svg)+'\n'


def main():
    assets = ROOT/'desktop/assets'
    public = ROOT/'desktop/frontend/public'
    for state, palette in STATES.items():
        image, svg = render_icon(*palette)
        (public/f'hqip-{state}.svg').write_text(svg, encoding='utf-8')
        image.resize((256,256), Image.Resampling.LANCZOS).save(assets/f'hqip-{state}.png')
        image.save(assets/f'hqip-{state}.ico', sizes=[(n,n) for n in (16,20,24,32,40,48,64,128,256)])
        if state == 'disconnected':
            (public/'hqip.svg').write_text(svg, encoding='utf-8')
            image.resize((256,256), Image.Resampling.LANCZOS).save(assets/'hqip.png')
            image.save(assets/'hqip.ico', sizes=[(n,n) for n in (16,20,24,32,40,48,64,128,256)])
    print('Generated five desktop status icons and the default app icon.')


if __name__ == '__main__':
    main()
