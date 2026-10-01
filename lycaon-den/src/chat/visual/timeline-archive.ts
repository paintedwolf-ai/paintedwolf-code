import { readZipEntries } from "./zip-entries.ts";

/** Timeline archives hold JPEG frames, a PNG poster, and a manifest of what the page did. */
export const TIMELINE_MIME = "application/vnd.lycaon.timeline+zip";

export function isTimelineMime(mime: string | null | undefined): boolean {
  return (mime ?? "").trim().toLowerCase() === TIMELINE_MIME;
}

export type TimelineBox = { x: number; y: number; width: number; height: number };

export type TimelineAction = {
  index: number;
  type: string;
  label: string;
  start_ms: number;
  end_ms: number;
  ok: boolean;
};

export type TimelineEvent = {
  at_ms: number;
  kind: string;
  detail?: Record<string, unknown>;
};

export type TimelineWatchSample = {
  at_ms: number;
  present: boolean;
  box?: TimelineBox;
  scroll_top?: number;
  scroll_left?: number;
};

export type TimelineWatchJump = {
  at_ms: number;
  dx?: number;
  dy?: number;
  dw?: number;
  dh?: number;
  scroll?: number;
};

export type TimelineSummary = {
  duration_ms: number;
  frame_count: number;
  visually_stable_at_ms: number;
  still_changing_at_end?: boolean;
  visual_changes?: Array<{ at_ms: number; fraction: number }>;
  layout_shift: {
    total: number;
    count: number;
    worst?: { at_ms: number; value: number };
    after_input: { total: number; count: number };
  };
  long_tasks: { count: number; total_ms: number; max_ms: number };
  watch?: Array<{
    selector: string;
    present: boolean;
    max_displacement_px: number;
    jumps?: TimelineWatchJump[];
    settled_at_ms: number;
  }>;
  requests: number;
  failed_requests?: number;
  errors?: number;
  sheet: Array<{ at_ms: number; frame: number; why: string }>;
};

export type TimelineManifest = {
  version: number;
  duration_ms: number;
  viewport: { width: number; height: number };
  frames: Array<{ at_ms: number; file: string; width: number; height: number }>;
  actions: TimelineAction[];
  events: TimelineEvent[];
  watch?: Array<{ selector: string; samples: TimelineWatchSample[] }>;
  summary: TimelineSummary;
};

export type TimelineFrame = {
  index: number;
  atMs: number;
  width: number;
  height: number;
  byteLength: number;
  /** Frame URL, retained until the recording is released. */
  src: string;
};

/** One decoded recording: its manifest, a URL per frame, and the poster sheet's URL. */
export type TimelineRecording = {
  manifest: TimelineManifest;
  frames: TimelineFrame[];
  poster?: string;
};

const FORMAT_VERSION = 1;

export async function unpackTimelineBlob(blob: Blob): Promise<TimelineRecording> {
  return unpackTimelineBytes(new Uint8Array(await blob.arrayBuffer()));
}

export async function unpackTimelineBytes(buf: Uint8Array): Promise<TimelineRecording> {
  const files = await readZipEntries(buf);
  const raw = files.get("manifest.json");
  if (!raw) throw new Error("timeline missing manifest.json");
  const manifest = JSON.parse(new TextDecoder().decode(raw)) as TimelineManifest;
  if (manifest.version !== FORMAT_VERSION) {
    throw new Error(`timeline manifest version ${String(manifest.version)} is not ${FORMAT_VERSION}`);
  }
  if (!Array.isArray(manifest.frames) || manifest.frames.length === 0) {
    throw new Error("timeline manifest has no frames");
  }
  const frames: TimelineFrame[] = [];
  try {
    manifest.frames.forEach((frame, index) => {
      const jpeg = files.get(frame.file);
      if (!jpeg) throw new Error(`timeline missing ${frame.file}`);
      const copy = new Uint8Array(jpeg.byteLength);
      copy.set(jpeg);
      frames.push({
        index,
        atMs: frame.at_ms,
        width: frame.width,
        height: frame.height,
        byteLength: copy.byteLength,
        src: URL.createObjectURL(new Blob([copy], { type: "image/jpeg" })),
      });
    });
  } catch (error) {
    revokeTimelineFrames(frames);
    throw error;
  }
  manifest.actions ??= [];
  manifest.events ??= [];
  const poster = files.get("poster.png");
  return {
    manifest,
    frames,
    poster: poster ? URL.createObjectURL(new Blob([poster.slice()], { type: "image/png" })) : undefined,
  };
}

export function revokeTimeline(recording: TimelineRecording): void {
  revokeTimelineFrames(recording.frames);
  if (recording.poster) URL.revokeObjectURL(recording.poster);
}

function revokeTimelineFrames(frames: readonly TimelineFrame[]): void {
  for (const frame of frames) URL.revokeObjectURL(frame.src);
}

/** The frame showing the page at a time: the last one painted at or before it. */
export function frameIndexAt(frames: readonly TimelineFrame[], atMs: number): number {
  let lo = 0;
  let hi = frames.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if ((frames[mid]?.atMs ?? Infinity) <= atMs) lo = mid;
    else hi = mid - 1;
  }
  return lo;
}

/** A moment in a recording, spelled as the host labels its poster cells. */
export function timelineClock(ms: number): string {
  return `${(Math.max(0, ms) / 1000).toFixed(2)}s`;
}
