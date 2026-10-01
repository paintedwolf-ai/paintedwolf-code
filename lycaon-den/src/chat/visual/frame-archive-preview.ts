import { isFilmstripMime } from "./filmstrip-zip.ts";
import { isTimelineMime } from "./timeline-archive.ts";
import { readZipEntry } from "./zip-entries.ts";

/** One image that stands for an artifact in a grid, and how to let it go. */
export type ArtifactPreview = { src: string; release(): void };

/** Preview still extracted from an artifact body (filmstrip, timeline poster, image, or video). */
export async function artifactPreview(body: Blob): Promise<ArtifactPreview> {
  if (isFilmstripMime(body.type)) {
    const buf = new Uint8Array(await body.arrayBuffer());
    return stillFrom(await filmstripFirstFrame(buf), "image/png");
  }
  if (isTimelineMime(body.type)) {
    const buf = new Uint8Array(await body.arrayBuffer());
    return stillFrom(await readZipEntry(buf, "poster.png"), "image/png");
  }
  const src = URL.createObjectURL(body);
  return { src, release: () => URL.revokeObjectURL(src) };
}

async function filmstripFirstFrame(buf: Uint8Array): Promise<Uint8Array | undefined> {
  const manifest = await readZipEntry(buf, "manifest.json");
  if (!manifest) return undefined;
  const parsed = JSON.parse(new TextDecoder().decode(manifest)) as { frames?: Array<{ file?: string }> };
  const file = parsed.frames?.[0]?.file;
  return file ? readZipEntry(buf, file) : undefined;
}

function stillFrom(bytes: Uint8Array | undefined, type: string): ArtifactPreview {
  if (!bytes) return { src: "", release: () => {} };
  const src = URL.createObjectURL(new Blob([bytes.slice()], { type }));
  return { src, release: () => URL.revokeObjectURL(src) };
}
