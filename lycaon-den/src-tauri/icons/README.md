# App icons

`icon.svg` supplies `icon.icns`. Its lit top edge and shaded bottom remain
visible at dock size. The artwork uses an 824×824 rounded square centred in
a 1024 canvas, radius 185.4. The 100px margin holds the cast shadow and sets
the icon's visual size in the dock.

The other raster icons and `icon.ico` use the full-bleed flat logomark. The
in-app logomark in `lycaon-den/public/favicon.svg` is a separate asset.

## Regenerating `icon.icns`

Needs `rsvg-convert` (`brew install librsvg`) and `iconutil` (bundled with
macOS).

```bash
cd lycaon-den/src-tauri/icons
work="$(mktemp -d)/icon.iconset" && mkdir -p "$work"
printf '%s\n' "16 icon_16x16" "32 icon_16x16@2x" "32 icon_32x32" \
  "64 icon_32x32@2x" "128 icon_128x128" "256 icon_128x128@2x" \
  "256 icon_256x256" "512 icon_256x256@2x" "512 icon_512x512" \
  "1024 icon_512x512@2x" |
  while read -r size name; do
    rsvg-convert -w "$size" -h "$size" icon.svg -o "$work/$name.png"
  done
iconutil -c icns "$work" -o icon.icns
```

The dock can continue displaying a cached icon after a bundle is rebuilt.

## `icon.icon` (macOS 26 and later)

`icon.icon` is the Icon Composer document for the layered icon macOS 26
draws with Liquid Glass. It holds only the field (a two-stop gradient in
`icon.json`) and the accent ramp (`Assets/ramp.svg`, the same bars as
`icon.svg` rescaled from the 824 square to the full 1024 canvas). The
system supplies the silhouette, edge light, and shadow, so none of the
`icon.svg` edge treatment belongs here, and the dark, clear, and tinted
appearances come from the same two layers.

The Tauri bundler compiles it into `Assets.car` with `actool` and sets
`CFBundleIconName`; `icon.icns` supplies the icon on supported systems before macOS 26.
That compile step needs Xcode 26 or later on the build machine — with an
older Xcode the bundler skips `Assets.car` with a warning and macOS 26 shows
the `.icns` inside a gray tile.

Debug and release builds use the configured layered icon. Both `./task den:app`
and `./task den:bundle` set `IBToolNeverDeque=1` to isolate asset compilation
from pooled service state.

Both paths put `scripts/macos-build-tools/actool` on their local `PATH`. The
launcher reopens stdin from `/dev/null` and calls the selected compiler through
`xcrun`, preserving arguments, diagnostics, and exit status. A closed stdin
descriptor can cause the compiler to exit zero without producing `Assets.car`;
see the [upstream descriptor diagnosis](https://github.com/tauri-apps/tauri/pull/15991).

Preview a rendition without building the app:

```bash
ictool="/Applications/Xcode.app/Contents/Applications/Icon Composer.app/Contents/Executables/ictool"
"$ictool" lycaon-den/src-tauri/icons/icon.icon --export-image --output-file /tmp/icon.png \
  --platform macOS --rendition Default --width 1024 --height 1024 --scale 1
```

Renditions: `Default`, `Dark`, `ClearLight`, `ClearDark`, `TintedLight`,
`TintedDark`.
