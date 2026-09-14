#!/usr/bin/env bash
#
# Regenerates the /flyer page images from the flyer PDF.
#
#   scripts/render-flyer.sh path/to/flyer.pdf
#
# The site shows the flyer as images rather than the PDF itself: Android Chrome
# will not render a PDF inside an iframe, iOS Safari handles one badly, and the
# CSP's object-src 'none' rules out <embed>/<object>. Those phones are exactly
# who arrives here, from the QR codes printed on the flyer.
#
# Each page is rendered at WIDTH px, trailing blank space is trimmed (page 2 of
# the print flyer ends in a handwriting area that is just empty white on a
# screen), and the result is encoded as WebP. Existing flyer-page-*.webp files
# are replaced, so a flyer with fewer pages cannot leave a stale one behind.
#
# Afterwards, update FlyerPages in handlers/flyer.go: the page count, and the
# width/height this prints, which the <img> tags use to reserve layout space.
#
# Needs uv (runs pypdfium2 + Pillow in a throwaway environment, nothing
# installed) and cwebp (brew install webp).

set -euo pipefail

SRC="${1:?usage: scripts/render-flyer.sh path/to/flyer.pdf}"
OUT="public/images"
WIDTH=1600
QUALITY=80

for tool in uv cwebp; do
	command -v "$tool" >/dev/null 2>&1 || {
		echo "FATAL required tool not found: $tool" >&2
		exit 1
	}
done
[ -f "$SRC" ] || { echo "FATAL no such file: $SRC" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

uv run --quiet --with pypdfium2 --with pillow python - "$SRC" "$work" "$WIDTH" <<'PY'
import sys

import pypdfium2 as pdfium
from PIL import ImageOps

src, out, width = sys.argv[1], sys.argv[2], int(sys.argv[3])
doc = pdfium.PdfDocument(src)
for i in range(len(doc)):
    page = doc[i]
    im = page.render(scale=width / page.get_size()[0]).to_pil().convert("RGB")
    # Anything darker than near-white counts as content; keep 60px of margin
    # below the last of it.
    ink = ImageOps.invert(im.convert("L")).point(lambda v: 255 if v > 12 else 0)
    bbox = ink.getbbox()
    if bbox:
        im = im.crop((0, 0, im.width, min(im.height, bbox[3] + 60)))
    im.save(f"{out}/flyer-page-{i + 1}.png")
    print(f"flyer-page-{i + 1}.webp  {im.width}x{im.height}")
PY

rm -f "$OUT"/flyer-page-*.webp
for png in "$work"/flyer-page-*.png; do
	cwebp -quiet -q "$QUALITY" "$png" -o "$OUT/$(basename "${png%.png}").webp"
done

ls -lh "$OUT"/flyer-page-*.webp | awk '{print $5, $NF}'
echo "now update FlyerPages in handlers/flyer.go to match the dimensions above"
