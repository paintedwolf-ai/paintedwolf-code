import { describe, expect, it, vi } from "vitest";
import { at } from "../../test/at.ts";
import {
  FILMSTRIP_MIME,
  unpackFilmstripBytes,
  unpackFilmstripZip,
  revokeFilmstripFrames,
} from "./filmstrip-zip.ts";
import { buildTestFilmstripZip, zipAsBlob } from "./filmstrip-zip.fixture.ts";

describe("filmstrip-zip", () => {
  it("shares identical image bytes across steps without losing their labels", async () => {
    const create = vi.spyOn(URL, "createObjectURL");
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    try {
      const frames = await unpackFilmstripBytes(buildTestFilmstripZip(false, 12));
      expect(frames).toHaveLength(12);
      expect(create).toHaveBeenCalledOnce();
      expect(new Set(frames.map((frame) => frame.src)).size).toBe(1);
      expect(frames[0]?.caption).toBe("initial");
      expect(frames[11]?.caption).toBe("Step 12");
      expect(frames.map((frame) => frame.index)).toEqual(Array.from({ length: 12 }, (_, i) => i));
      revokeFilmstripFrames(frames);
      expect(revoke).toHaveBeenCalledOnce();
    } finally { vi.restoreAllMocks(); }
  });
  it("unpacks store-method filmstrip bytes", async () => {
    const frames = await unpackFilmstripBytes(buildTestFilmstripZip());
    expect(frames).toHaveLength(2);
    expect(at(frames, 0).caption).toBe("initial");
    expect(at(frames, 1).caption).toBe("click #load");
    revokeFilmstripFrames(frames);
  });

  it("unpacks a filmstrip Blob", async () => {
    const frames = await unpackFilmstripZip(
      zipAsBlob(buildTestFilmstripZip(), FILMSTRIP_MIME),
    );
    expect(frames).toHaveLength(2);
    revokeFilmstripFrames(frames);
  });

  it("revokes frames created before a malformed manifest entry fails", async () => {
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:first");
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});

    await expect(unpackFilmstripBytes(buildTestFilmstripZip(true))).rejects.toThrow(
      "filmstrip missing 001.png",
    );

    expect(create).toHaveBeenCalledOnce();
    expect(revoke).toHaveBeenCalledWith("blob:first");
    vi.restoreAllMocks();
  });
});
