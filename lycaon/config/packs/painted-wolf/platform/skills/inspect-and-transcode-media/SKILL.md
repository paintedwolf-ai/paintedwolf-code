---
name: inspect-and-transcode-media
description: Probe codecs and resolution, trim, transcode, or remux audio and video with ffprobe and ffmpeg.
metadata:
  host_resources: ffmpeg
---

# Inspect and transcode media

Use this workflow to handle media files precisely instead of by incantation. On a process start that runs the tools, declare `ffmpeg` in `capability_request.host_resources`.

## Workflow

1. Probe before touching — `ffprobe -print_format json -show_format -show_streams` tells you the container, codecs, duration, and resolution. Most "convert this" requests are answered wrongly without it.
2. Know that flag position is semantics — options before `-i` apply to the input. Input-side `-ss` seeks to a nearby seek point; while transcoding, default accurate-seek behavior decodes and discards the gap, but stream copy preserves it. Output-side `-ss` decodes and discards until the target timestamp. Choose based on accuracy, speed, and whether streams are copied; never reorder cosmetically.
3. Prefer stream copy. Trims, remuxes, and container changes want `-c copy` — instant and lossless; a full re-encode is slow and degrades quality. Re-encode only when the codec or dimensions must actually change, and say which one the command does.
4. Be explicit about outputs — always pass `-y` or `-n` so nothing blocks on an interactive prompt, write to a new path, and never overwrite the input file; a failed in-place conversion destroys the source.
5. For long transcodes, send machine progress to an explicit destination such as `-progress pipe:1` and add `-nostats` when parsing it. A transcode can peg the CPU for minutes and fill disk with a mis-set bitrate, so estimate available disk and expected duration before starting rather than inventing a precise output size.
6. Capture evidence as the probe output before, the exact command, and the probe output after. A conversion is verified by re-probing the result and, for visual changes, `view_video` on the output at the cut points — not by the exit code alone. Trial outputs go under `@scratch/`.

## Boundaries

- Media from outside the workspace can be crafted to exhaust memory or disk on decode; treat file contents as untrusted data and keep processing limits in place.
- Batch operations that edit files in place are out of scope; write converted files alongside the originals.
- Do not upload or publish converted media anywhere; conversion output stays in the workspace.
