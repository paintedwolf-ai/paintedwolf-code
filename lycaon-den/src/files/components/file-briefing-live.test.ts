import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  applyFileBriefingEvent,
  getLiveFileBriefing,
  markFileBriefingSourceChanged,
  receiveFileBriefingSnapshot,
  resetFileBriefingLiveForTest,
  subscribeFileBriefings,
} from "./file-briefing-live.ts";

const preview = {
  language: "go",
  line_count: 20,
};

const briefingFields = {
  target_key: "target-1",
  attempt_id: "11111111-1111-4111-8111-111111111111",
  presentation: "current" as const,
  sections: [],
  fallback_text: "",
  truncated: false,
};

const eventFields = {
  target_key: "target-1",
  attempt_id: "11111111-1111-4111-8111-111111111111",
  presentation: "current" as const,
  fallback_text: "",
  truncated: false,
};
const currentRequest = '{"root_id":"r1","path":"main.go","presentation":"current"}';

describe("file briefing live store", () => {
  beforeEach(resetFileBriefingLiveForTest);

  it("appends revision-matched deltas and seals with validated sections", () => {
    const listener = vi.fn();
    subscribeFileBriefings(listener);
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "pending",
      preview,
      locations: [{ line: 8, name: "Serve", kind: "function" }],
      updated_at: "2026-08-14T00:00:00Z",
    }, currentRequest);
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "streaming",
      delta: "Purpose: builds ",
      updated_at: "2026-08-14T00:00:01Z",
    });
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "streaming",
      delta: "the app.",
      updated_at: "2026-08-14T00:00:02Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)?.streamText).toBe(
      "Purpose: builds the app.",
    );
    expect(getLiveFileBriefing("p1", currentRequest)?.locations).toEqual([
      { line: 8, name: "Serve", kind: "function" },
    ]);
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "complete",
      preview,
      sections: [{ kind: "purpose", text: "Final explanation." }],
      updated_at: "2026-08-14T00:00:03Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)).toMatchObject({
      status: "complete",
      sections: [{ kind: "purpose", text: "Final explanation." }],
    });
    expect(getLiveFileBriefing("p1", currentRequest)?.streamText).toBeUndefined();
    expect(getLiveFileBriefing("p1", currentRequest)?.locations).toEqual([
      { line: 8, name: "Serve", kind: "function" },
    ]);

    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "streaming",
      delta: "stale fragment",
      updated_at: "2026-08-14T00:00:02Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)).toMatchObject({
      status: "complete",
      sections: [{ kind: "purpose", text: "Final explanation." }],
    });
    expect(listener).toHaveBeenCalled();
  });

  it("rejects late updates from an earlier generation attempt", () => {
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "pending",
      preview,
      locations: [],
      updated_at: "2026-08-14T00:00:00Z",
    }, currentRequest);
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      attempt_id: "22222222-2222-4222-8222-222222222222",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "pending",
      preview,
      locations: [],
      updated_at: "2026-08-14T00:00:01Z",
    }, currentRequest);
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "streaming",
      delta: "stale fragment",
      updated_at: "2026-08-14T00:00:02Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)).toMatchObject({
      attempt_id: "22222222-2222-4222-8222-222222222222",
      status: "pending",
    });
    expect(getLiveFileBriefing("p1", currentRequest)?.streamText).toBeUndefined();
  });

  it("marks only the changed source revision stale", () => {
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "complete",
      preview,
      locations: [],
      updated_at: "2026-08-14T00:00:00Z",
    }, currentRequest);
    markFileBriefingSourceChanged("p1", {
      root_id: "r1",
      path: "main.go",
      op: "write",
      origin: "user",
      after_sha256: "sha-2",
      changed_at: "2026-08-14T00:00:01Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)?.stale).toBe(true);

    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "complete",
      updated_at: "2026-08-14T00:00:02Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)?.stale).toBe(true);
  });

  it("does not stale immutable document or retained-version briefings", () => {
    for (const presentation of ["document", "version"] as const) {
      const identity = `${presentation}-request`;
      receiveFileBriefingSnapshot("p1", {
        ...briefingFields,
        target_key: `${presentation}-target`,
        presentation,
        root_id: "r1",
        path: "main.go",
        source_sha256: "sha-1",
        status: "complete",
        preview,
        locations: [],
        updated_at: "2026-08-14T00:00:00Z",
      }, identity);
      markFileBriefingSourceChanged("p1", {
        root_id: "r1",
        path: "main.go",
        op: "write",
        origin: "user",
        after_sha256: "sha-2",
        changed_at: "2026-08-14T00:00:01Z",
      });
      expect(getLiveFileBriefing("p1", identity)?.stale).toBe(false);
    }
  });

  it("keeps exact targets separate for the same file", () => {
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "complete",
      preview,
      locations: [],
      sections: [{ kind: "purpose", text: "First." }],
      updated_at: "2026-08-14T00:00:00Z",
    }, "version-1");
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      target_key: "target-2",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-2",
      status: "complete",
      preview,
      locations: [],
      sections: [{ kind: "purpose", text: "Second." }],
      updated_at: "2026-08-14T00:00:01Z",
    }, "version-2");

    expect(getLiveFileBriefing("p1", "version-1")?.sections[0]?.text).toBe("First.");
    expect(getLiveFileBriefing("p1", "version-2")?.sections[0]?.text).toBe("Second.");
  });

  it("keeps streamed explanation when the terminal result has no sections", () => {
    receiveFileBriefingSnapshot("p1", {
      ...briefingFields,
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "pending",
      preview,
      locations: [],
      updated_at: "2026-08-14T00:00:00Z",
    }, currentRequest);
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "streaming",
      delta: "Purpose: builds the app.",
      updated_at: "2026-08-14T00:00:01Z",
    });
    applyFileBriefingEvent({
      ...eventFields,
      project_id: "p1",
      root_id: "r1",
      path: "main.go",
      source_sha256: "sha-1",
      status: "preview",
      preview,
      updated_at: "2026-08-14T00:00:02Z",
    });
    expect(getLiveFileBriefing("p1", currentRequest)).toMatchObject({
      status: "preview",
      streamText: "Purpose: builds the app.",
      sections: [],
    });
  });

  it("bounds settled in-process cache entries", () => {
    for (let index = 0; index < 129; index += 1) {
      receiveFileBriefingSnapshot("p1", {
        ...briefingFields,
        target_key: `target-${index}`,
        root_id: "r1",
        path: `file-${index}.go`,
        source_sha256: `sha-${index}`,
        status: "complete",
        preview,
        locations: [],
        updated_at: new Date(Date.UTC(2026, 7, 14, 0, 0, index)).toISOString(),
      }, `request-${index}`);
    }
    expect(getLiveFileBriefing("p1", "request-0")).toBeNull();
    expect(getLiveFileBriefing("p1", "request-128")?.target_key).toBe("target-128");
  });

  it("bounds in-flight in-process cache entries", () => {
    for (let index = 0; index < 129; index += 1) {
      receiveFileBriefingSnapshot("p1", {
        ...briefingFields,
        target_key: `pending-${index}`,
        root_id: "r1",
        path: `pending-${index}.go`,
        source_sha256: `pending-sha-${index}`,
        status: "pending",
        preview,
        locations: [],
        updated_at: new Date(Date.UTC(2026, 7, 14, 0, 0, index)).toISOString(),
      }, `pending-request-${index}`);
    }
    expect(getLiveFileBriefing("p1", "pending-request-0")).toBeNull();
    expect(getLiveFileBriefing("p1", "pending-request-128")?.target_key).toBe(
      "pending-128",
    );
  });

  it("bounds request identities that share one briefing", () => {
    for (let index = 0; index < 257; index += 1) {
      receiveFileBriefingSnapshot("p1", {
        ...briefingFields,
        root_id: "r1",
        path: "main.go",
        source_sha256: "sha-1",
        status: "complete",
        preview,
        locations: [],
        updated_at: "2026-08-14T00:00:00Z",
      }, `request-${index}`);
    }
    expect(getLiveFileBriefing("p1", "request-0")).toBeNull();
    expect(getLiveFileBriefing("p1", "request-256")?.target_key).toBe("target-1");
  });
});
