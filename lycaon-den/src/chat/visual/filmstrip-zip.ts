import { readZipEntries } from "./zip-entries.ts";

/** Filmstrip archives contain PNG frames and a JSON manifest. */
export const FILMSTRIP_MIME = "application/vnd.lycaon.filmstrip+zip";

export function isFilmstripMime(mime: string | null | undefined): boolean {
  return (mime ?? "").trim().toLowerCase() === FILMSTRIP_MIME;
}

export type FilmstripFrame = {
  index: number;
  byteLength: number;
  caption: string;
  /** Frame URL, retained until the decoded frame collection is released. */
  src: string;
};

type Manifest = {
  version?: number;
  frames: Array<{ index: number; file: string; caption?: string }>;
};

export async function unpackFilmstripZip(blob: Blob): Promise<FilmstripFrame[]> {
  const buf = new Uint8Array(await blob.arrayBuffer());
  return unpackFilmstripBytes(buf);
}

export async function unpackFilmstripBytes(buf: Uint8Array): Promise<FilmstripFrame[]> {
  const files = await readZipEntries(buf);
  const manRaw = files.get("manifest.json");
  if (!manRaw) {
    throw new Error("filmstrip missing manifest.json");
  }
  const man = JSON.parse(new TextDecoder().decode(manRaw)) as Manifest;
  if (!Array.isArray(man.frames) || man.frames.length === 0) {
    throw new Error("filmstrip manifest has no frames");
  }
  const out: FilmstripFrame[] = [];
  const images = new Map<string, string>();
  try {
    for (const fr of man.frames) {
      const png = files.get(fr.file);
      if (!png) {
        throw new Error(`filmstrip missing ${fr.file}`);
      }
      const copy = new Uint8Array(png.byteLength);
      copy.set(png);
      // Identical PNGs share a URL and decoded-image cache.
      const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", copy));
      const key = Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join("");
      let src = images.get(key);
      if (!src) {
        src = URL.createObjectURL(new Blob([copy], { type: "image/png" }));
        images.set(key, src);
      }
      out.push({
        index: fr.index,
        byteLength: copy.byteLength,
        caption: (fr.caption ?? "").trim(),
        src,
      });
    }
    return out;
  } catch (error) {
    for (const src of images.values()) URL.revokeObjectURL(src);
    throw error;
  }
}

export function revokeFilmstripFrames(frames: readonly FilmstripFrame[]): void {
  for (const src of new Set(frames.map((frame) => frame.src))) URL.revokeObjectURL(src);
}
