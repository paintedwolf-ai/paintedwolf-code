import { describe, expect, it, vi } from "vitest";
import { artifactPreview } from "./frame-archive-preview.ts";
import { buildStoredZip, buildTestFilmstripZip, zipAsBlob } from "./filmstrip-zip.fixture.ts";
import { FILMSTRIP_MIME } from "./filmstrip-zip.ts";
import { buildTestTimelineZip, testTimelineManifest } from "./timeline-archive.fixture.ts";
import { TIMELINE_MIME } from "./timeline-archive.ts";

/** Marks one entry's central-directory method as unreadable, so decoding it would throw. */
function corruptEntry(zip: Uint8Array, name: string): Uint8Array {
  const out = zip.slice();
  const view = new DataView(out.buffer);
  const needle = new TextEncoder().encode(name);
  for (let i = out.length - 22; i >= 0; i--) {
    if (view.getUint32(i, true) !== 0x02014b50) continue;
    const nameLen = view.getUint16(i + 28, true);
    const entryName = out.subarray(i + 46, i + 46 + nameLen);
    if (nameLen === needle.length && entryName.every((byte, index) => byte === needle[index])) {
      view.setUint16(i + 10, 99, true);
      return out;
    }
  }
  throw new Error(`fixture entry not found: ${name}`);
}

describe("artifact preview", () => {
  it("reads only a timeline's poster, leaving its frames unread", async () => {
    const manifest = testTimelineManifest();
    const zip = buildStoredZip([
      { name: "manifest.json", data: new TextEncoder().encode(JSON.stringify(manifest)) },
      { name: "poster.png", data: new TextEncoder().encode("poster bytes") },
      ...manifest.frames.map((frame) => ({ name: frame.file, data: new Uint8Array([1, 2, 3]) })),
    ]);
    const create = vi.spyOn(URL, "createObjectURL");
    try {
      const preview = await artifactPreview(zipAsBlob(corruptEntry(zip, "frames/0001.jpg"), TIMELINE_MIME));
      expect(preview.src).not.toBe("");
      expect(create).toHaveBeenCalledOnce();
      preview.release();
    } finally {
      vi.restoreAllMocks();
    }
  });

  it("reads only a filmstrip's first frame", async () => {
    const create = vi.spyOn(URL, "createObjectURL");
    try {
      const preview = await artifactPreview(zipAsBlob(corruptEntry(buildTestFilmstripZip(false, 3), "002.png"), FILMSTRIP_MIME));
      expect(preview.src).not.toBe("");
      expect(create).toHaveBeenCalledOnce();
      preview.release();
    } finally {
      vi.restoreAllMocks();
    }
  });

  it("stands for a timeline without a poster by nothing rather than by decoding it", async () => {
    const preview = await artifactPreview(zipAsBlob(buildTestTimelineZip(), TIMELINE_MIME));
    expect(preview.src).toBe("");
    preview.release();
  });
});
