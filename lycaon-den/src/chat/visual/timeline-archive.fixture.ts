import { buildStoredZip } from "./filmstrip-zip.fixture.ts";
import type { TimelineManifest } from "./timeline-archive.ts";

/** A 1×1 baseline JPEG; frame bytes only need to exist for the decoder under test. */
const TINY_JPEG = Uint8Array.from([
  0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00, ...new Array<number>(64).fill(1),
  0xff, 0xc0, 0x00, 0x0b, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00,
  0xff, 0xda, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3f, 0x00, 0xd2, 0xcf, 0x20, 0xff, 0xd9,
]);

/** A recording of a click followed 800 ms later by a banner that pushes the content down. */
export function testTimelineManifest(frameTimes: readonly number[] = [0, 120, 900, 1400]): TimelineManifest {
  return {
    version: 1,
    duration_ms: 1500,
    viewport: { width: 800, height: 600 },
    frames: frameTimes.map((at_ms, i) => ({ at_ms, file: `frames/${String(i).padStart(4, "0")}.jpg`, width: 800, height: 600 })),
    actions: [{ index: 0, type: "click", label: "click #go", start_ms: 100, end_ms: 140, ok: true }],
    events: [
      { at_ms: 130, kind: "layout_shift", detail: { value: 0.01, had_recent_input: true } },
      { at_ms: 900, kind: "layout_shift", detail: { value: 0.18, had_recent_input: false } },
      { at_ms: 910, kind: "long_task", detail: { duration_ms: 72 } },
      { at_ms: 300, kind: "request", detail: { method: "GET", url: "/api/items", status: 500 } },
      { at_ms: 320, kind: "error", detail: { message: "status 500" } },
    ],
    watch: [{ selector: "#content", samples: [
      { at_ms: 0, present: true, box: { x: 0, y: 20, width: 800, height: 200 } },
      { at_ms: 900, present: true, box: { x: 0, y: 140, width: 800, height: 200 } },
    ] }],
    summary: {
      duration_ms: 1500,
      frame_count: frameTimes.length,
      visually_stable_at_ms: 900,
      visual_changes: [{ at_ms: 120, fraction: 0.02 }, { at_ms: 900, fraction: 0.31 }],
      layout_shift: { total: 0.18, count: 1, worst: { at_ms: 900, value: 0.18 }, after_input: { total: 0.01, count: 1 } },
      long_tasks: { count: 1, total_ms: 72, max_ms: 72 },
      watch: [{ selector: "#content", present: true, max_displacement_px: 120, jumps: [{ at_ms: 900, dy: 120 }], settled_at_ms: 900 }],
      requests: 1,
      failed_requests: 1,
      errors: 1,
      sheet: [{ at_ms: 0, frame: 0, why: "start" }, { at_ms: 900, frame: 2, why: "last change" }],
    },
  };
}

export function buildTestTimelineZip(manifest: TimelineManifest = testTimelineManifest()): Uint8Array {
  return buildStoredZip([
    { name: "manifest.json", data: new TextEncoder().encode(JSON.stringify(manifest)) },
    ...manifest.frames.map((frame) => ({ name: frame.file, data: TINY_JPEG })),
  ]);
}
