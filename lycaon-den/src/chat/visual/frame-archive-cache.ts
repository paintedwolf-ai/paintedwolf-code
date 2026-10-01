import type { LycaonClient } from "../../api/client.ts";
import { measureSync, perfMark } from "../stream/den-main-thread-perf.ts";
import { type FilmstripFrame, revokeFilmstripFrames, unpackFilmstripZip } from "./filmstrip-zip.ts";
import { revokeTimeline, type TimelineRecording, unpackTimelineBlob } from "./timeline-archive.ts";

const IDLE_BYTES = 32 * 1024 * 1024;
const IDLE_ENTRIES = 12;
const IDLE_FRAMES = 128;

/** How one archive format decodes, releases, and weighs its frames. */
export type FrameArchiveCodec<T> = {
  /** Names the format in performance marks. */
  name: string;
  unpack(blob: Blob): Promise<T>;
  release(value: T): void;
  frames(value: T): readonly { byteLength: number }[];
};

type Entry<T> = {
  client: LycaonClient;
  sessionId: string;
  artifactId: string;
  readers: number;
  retired: boolean;
  value?: T;
  ready: Promise<T | undefined>;
};

export type FrameArchiveLease<T> = {
  /** Resolves undefined when the archive was released before it finished decoding. */
  readonly ready: Promise<T | undefined>;
  readonly value: T | undefined;
  release(): void;
};

/** Mounted readers retain their frame URLs; a bounded idle LRU bridges virtual row remounts. */
export class FrameArchiveCache<T> {
  private readonly entries = new Set<Entry<T>>();

  constructor(
    private readonly codec: FrameArchiveCodec<T>,
    private readonly maxIdleBytes = IDLE_BYTES,
    private readonly maxIdleEntries = IDLE_ENTRIES,
    private readonly maxIdleFrames = IDLE_FRAMES,
  ) {}

  acquire(client: LycaonClient, sessionId: string, artifactId: string): FrameArchiveLease<T> {
    const sid = sessionId.trim();
    const id = artifactId.trim();
    if (!sid || !id) throw new Error(`${this.codec.name} requires a session and artifact id`);
    let entry = [...this.entries].find((candidate) =>
      candidate.client === client && candidate.sessionId === sid && candidate.artifactId === id);
    if (!entry) {
      entry = this.load(client, sid, id);
      this.entries.add(entry);
    }
    const current = entry;
    current.readers++;
    this.touch(current);
    this.trim();
    let released = false;
    return {
      ready: current.ready,
      get value() { return current.value; },
      release: () => {
        if (released) return;
        released = true;
        current.readers--;
        this.touch(current);
        this.trim();
      },
    };
  }

  invalidate(artifactId: string): void {
    for (const entry of this.entries) {
      if (entry.artifactId === artifactId.trim()) this.retire(entry);
    }
  }

  clear(): void {
    for (const entry of this.entries) this.retire(entry);
  }

  private load(client: LycaonClient, sessionId: string, artifactId: string): Entry<T> {
    const entry: Entry<T> = {
      client, sessionId, artifactId, readers: 0, retired: false,
      ready: Promise.resolve(undefined),
    };
    entry.ready = this.read(entry).catch((error: unknown) => {
      this.retire(entry);
      throw error;
    });
    return entry;
  }

  private async read(entry: Entry<T>): Promise<T | undefined> {
    perfMark(`${this.codec.name}.load`, { retained: this.entries.size });
    const blob = await entry.client.getSessionArtifact(entry.sessionId, entry.artifactId);
    if (entry.retired) return undefined;
    const value = await this.codec.unpack(blob);
    if (entry.retired) {
      this.codec.release(value);
      return undefined;
    }
    entry.value = value;
    perfMark(`${this.codec.name}.ready`, { frames: this.frameCount(entry), bytes: this.bytes(entry) });
    this.trim();
    return entry.retired ? undefined : value;
  }

  private bytes(entry: Entry<T>): number {
    return entry.value === undefined ? 0 : this.codec.frames(entry.value).reduce((sum, frame) => sum + frame.byteLength, 0);
  }

  private frameCount(entry: Entry<T>): number {
    return entry.value === undefined ? 0 : this.codec.frames(entry.value).length;
  }

  private touch(entry: Entry<T>): void {
    if (entry.retired) return;
    this.entries.delete(entry);
    this.entries.add(entry);
  }

  private trim(): void {
    const idle = [...this.entries].filter((entry) => entry.readers === 0);
    let bytes = idle.reduce((sum, entry) => sum + this.bytes(entry), 0);
    let count = idle.length;
    let frames = idle.reduce((sum, entry) => sum + this.frameCount(entry), 0);
    for (const entry of idle) {
      if (bytes <= this.maxIdleBytes && count <= this.maxIdleEntries && frames <= this.maxIdleFrames) break;
      bytes -= this.bytes(entry);
      count--;
      frames -= this.frameCount(entry);
      this.retire(entry);
    }
  }

  private retire(entry: Entry<T>): void {
    if (entry.retired) return;
    entry.retired = true;
    this.entries.delete(entry);
    const value = entry.value;
    if (value !== undefined) measureSync(`${this.codec.name}.release`, () => this.codec.release(value));
    entry.value = undefined;
  }
}

export const filmstripCodec: FrameArchiveCodec<FilmstripFrame[]> = {
  name: "filmstrip",
  unpack: unpackFilmstripZip,
  release: revokeFilmstripFrames,
  frames: (frames) => frames,
};

export const timelineCodec: FrameArchiveCodec<TimelineRecording> = {
  name: "timeline",
  unpack: unpackTimelineBlob,
  release: revokeTimeline,
  frames: (recording) => recording.frames,
};

export const filmstripCache = new FrameArchiveCache(filmstripCodec);
export const timelineCache = new FrameArchiveCache(timelineCodec);

/** Releases every decoded archive, as on a reveal reset. */
export function clearFrameArchives(): void {
  filmstripCache.clear();
  timelineCache.clear();
}

/** Drops an artifact's decoded frames after its bytes changed or were deleted. */
export function invalidateFrameArchive(artifactId: string): void {
  filmstripCache.invalidate(artifactId);
  timelineCache.invalidate(artifactId);
}
