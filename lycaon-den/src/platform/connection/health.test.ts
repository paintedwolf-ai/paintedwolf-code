import { describe, expect, it, vi } from "vitest";
import {
  compareSemver,
  denVersionIsOlderThanMin,
  lastSeenSchemaVersion,
  lastSeenStoreRevision,
  noteHealthResponse,
  noteStoreRevision,
  resetStoreRevisionTracking,
} from "./health.ts";

describe("store revision tracking", () => {
  it("does not signal change on first note", () => {
    resetStoreRevisionTracking();
    expect(noteStoreRevision(3)).toBe(false);
    expect(lastSeenStoreRevision()).toBe(3);
  });

  it("signals change when revision advances", () => {
    resetStoreRevisionTracking();
    noteStoreRevision(1);
    expect(noteStoreRevision(2)).toBe(true);
  });

  it("does not signal change when revision is unchanged", () => {
    resetStoreRevisionTracking();
    noteStoreRevision(5);
    expect(noteStoreRevision(5)).toBe(false);
  });
});

describe("compareSemver / den skew", () => {
  it("orders dotted versions", () => {
    expect(compareSemver("0.1.0", "0.2.0")).toBe(-1);
    expect(compareSemver("1.0.0", "1.0.0")).toBe(0);
    expect(compareSemver("1.2.3", "1.2.0")).toBe(1);
  });

  it("orders prereleases with complete semantic version precedence", () => {
    expect(compareSemver("1.0.0-rc.1", "1.0.0-rc.2")).toBe(-1);
    expect(compareSemver("1.0.0-rc.2", "1.0.0")).toBe(-1);
    expect(compareSemver("1.0.0+build.2", "1.0.0+build.1")).toBe(0);
  });

  it("rejects the tag prefix in product versions", () => {
    expect(compareSemver("v1.0.0", "1.0.0")).toBe(-1);
  });

  it("treats missing min_den_version as no skew", () => {
    expect(denVersionIsOlderThanMin(undefined)).toBe(false);
    expect(denVersionIsOlderThanMin("")).toBe(false);
  });

  it("detects older Den vs min floor", () => {
    expect(denVersionIsOlderThanMin("9.9.9", "0.1.0")).toBe(true);
    expect(denVersionIsOlderThanMin("0.0.1", "0.1.0")).toBe(false);
  });
});

describe("noteHealthResponse", () => {
  it("records schema_version and publishes one-shot skew notice", () => {
    resetStoreRevisionTracking();
    const first = noteHealthResponse({
      status: "ok",
      version: "0.1.0",
      store_revision: 1,
      schema_version: 4,
      min_den_version: "99.0.0",
      recovery_snapshot_available: false,
    });
    expect(lastSeenSchemaVersion()).toBe(4);
    expect(first?.code).toBe("den_version_skew");
    expect(first?.severity).toBe("warning");
    expect(first?.title).toBe("Update Painted Wolf Code");

    const second = noteHealthResponse({
      status: "ok",
      version: "0.1.0",
      store_revision: 1,
      schema_version: 4,
      min_den_version: "99.0.0",
      recovery_snapshot_available: false,
    });
    expect(second).toBeNull();
  });

  it("ignores unknown extra keys on the health envelope", () => {
    resetStoreRevisionTracking();
    const notice = noteHealthResponse({
      status: "ok",
      version: "0.1.0",
      store_revision: 1,
      schema_version: 1,
      future_field: "ok",
    } as never);
    expect(notice).toBeNull();
    expect(lastSeenSchemaVersion()).toBe(1);
  });
});

describe("fetchHealth", () => {
  it("loads health JSON from the sidecar", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () =>
      new Response(
        JSON.stringify({
          status: "ok",
          version: "0.1.0",
          store_revision: 7,
          schema_version: 1,
          unknown_extra: true,
        }),
        { status: 200 },
      ),
    ) as typeof fetch;

    const { fetchHealth } = await import("./backend.ts");
    const health = await fetchHealth("http://127.0.0.1:8787/");
    expect(health.store_revision).toBe(7);
    expect(health.schema_version).toBe(1);

    globalThis.fetch = originalFetch;
  });
});
