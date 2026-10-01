import { describe, expect, it } from "vitest";
import {
  clearThumbnails,
  dropThumbnail,
  parseThumbnails,
  putThumbnail,
  shouldRecaptureThumbnail,
  THUMBNAIL_MIN_RECAPTURE_MS,
  type ThumbnailMap,
} from "./thumbnail-cache.ts";

describe("putThumbnail", () => {
  it("inserts and replaces by project id", () => {
    let map: ThumbnailMap = {};
    map = putThumbnail(map, "a", "data:a1", 1);
    map = putThumbnail(map, "a", "data:a2", 2);
    expect(map.a).toEqual({ dataUrl: "data:a2", capturedAt: 2 });
    expect(Object.keys(map)).toEqual(["a"]);
  });

  it("evicts the oldest entries past the cap", () => {
    let map: ThumbnailMap = {};
    map = putThumbnail(map, "old", "data:old", 1, 2);
    map = putThumbnail(map, "mid", "data:mid", 2, 2);
    map = putThumbnail(map, "new", "data:new", 3, 2);
    expect(Object.keys(map).sort()).toEqual(["mid", "new"]);
    expect(map.old).toBeUndefined();
  });
});

describe("dropThumbnail", () => {
  it("removes an entry and is a no-op for unknown ids", () => {
    const map = putThumbnail({}, "a", "data:a", 1);
    expect(dropThumbnail(map, "a")).toEqual({});
    expect(dropThumbnail(map, "missing")).toBe(map);
  });
});

describe("clearThumbnails", () => {
  it("empties a populated map and is a no-op for empty", () => {
    const map = putThumbnail({}, "a", "data:a", 1);
    expect(clearThumbnails(map)).toEqual({});
    const empty: ThumbnailMap = {};
    expect(clearThumbnails(empty)).toBe(empty);
  });
});

describe("shouldRecaptureThumbnail", () => {
  it("captures when no entry exists", () => {
    expect(shouldRecaptureThumbnail({}, "a", 1000)).toBe(true);
  });

  it("skips a recapture inside the min interval", () => {
    const map = putThumbnail({}, "a", "data:a", 1000);
    expect(shouldRecaptureThumbnail(map, "a", 1000 + THUMBNAIL_MIN_RECAPTURE_MS - 1)).toBe(
      false,
    );
  });

  it("allows a recapture once the interval has elapsed", () => {
    const map = putThumbnail({}, "a", "data:a", 1000);
    expect(shouldRecaptureThumbnail(map, "a", 1000 + THUMBNAIL_MIN_RECAPTURE_MS)).toBe(true);
  });
});

describe("parseThumbnails", () => {
  it("parses persisted entries and tolerates junk", () => {
    const map = putThumbnail({}, "a", "data:a", 1);
    expect(parseThumbnails(JSON.stringify(map))).toEqual(map);
    expect(parseThumbnails(null)).toEqual({});
    expect(parseThumbnails("not json")).toEqual({});
    expect(parseThumbnails(JSON.stringify({ a: { dataUrl: 5 } }))).toEqual({});
  });
});
