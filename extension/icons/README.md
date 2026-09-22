# foldex icons

`fx-16/32/48/128.png` are the "fx" wordmark: `#1B1B2F` rounded-square
background (corner radius = 30% of the size, matching the 30×30 r9 logo in
the popup header) with a white lowercase "fx" set in Outfit 800.

## Regenerating

The PNGs are committed, so this is only needed when the wordmark changes.
Requires ImageMagick (`magick`) and network access to fetch the font once:

```sh
curl -fsSL -o /tmp/Outfit-800.ttf \
  'https://cdn.jsdelivr.net/fontsource/fonts/outfit@latest/latin-800-normal.ttf'

gen() {
  magick -size $1x$1 xc:none \
    -fill '#1B1B2F' -draw "roundrectangle 0,0,$(($1-1)),$(($1-1)),$2,$2" \
    -font /tmp/Outfit-800.ttf -fill white -pointsize $3 \
    -gravity center -annotate +0-1 'fx' -depth 8 fx-$1.png
}
gen 16 5 8
gen 32 10 15
gen 48 14 22
gen 128 38 58
```

The type sizes were tuned by eye against the popup header logo; keep the
letters optically centered (the `+0-1` nudge) when changing sizes.
