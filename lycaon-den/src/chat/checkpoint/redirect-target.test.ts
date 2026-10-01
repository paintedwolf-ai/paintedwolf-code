import { describe, expect, it } from "vitest";
import { pickRedirectTarget } from "./redirect-target.ts";
import type { PendingCheckpoint } from "./checkpoint-model.ts";

function checkpoint(
  checkpointId: string,
  kind: PendingCheckpoint["kind"],
): PendingCheckpoint {
  return {
    checkpointId,
    sessionId: "sess-1",
    kind,
    status: "pending",
    issuedAt: "t",
  } as PendingCheckpoint;
}

describe("pickRedirectTarget", () => {
  it("returns undefined with nothing pending", () => {
    expect(pickRedirectTarget([], null, null)).toBeUndefined();
    expect(pickRedirectTarget([], "chk-1", null)).toBeUndefined();
  });

  it("the focused card wins while it is still pending", () => {
    const pending = [
      checkpoint("chk-1", "tool_approval"),
      checkpoint("chk-2", "content_apply"),
      checkpoint("chk-3", "content_apply"),
    ];
    expect(pickRedirectTarget(pending, "chk-2", null)?.checkpointId).toBe(
      "chk-2",
    );
  });

  it("stale focus falls back to the newest pending card", () => {
    const pending = [
      checkpoint("chk-1", "tool_approval"),
      checkpoint("chk-2", "content_apply"),
    ];
    expect(
      pickRedirectTarget(pending, "chk-resolved", null)?.checkpointId,
    ).toBe("chk-2");
  });

  it("no focus falls back to the newest pending card", () => {
    const pending = [
      checkpoint("chk-1", "tool_approval"),
      checkpoint("chk-2", "tool_approval"),
    ];
    expect(pickRedirectTarget(pending, null, null)?.checkpointId).toBe(
      "chk-2",
    );
  });

  it("the dock-visible card outranks the newest pending fallback", () => {
    const pending = [
      checkpoint("chk-1", "tool_approval"),
      checkpoint("chk-2", "tool_approval"),
      checkpoint("chk-3", "tool_approval"),
    ];
    // The visible dock card takes precedence over queued cards.
    expect(pickRedirectTarget(pending, null, "chk-1")?.checkpointId).toBe(
      "chk-1",
    );
    // Explicit card focus still wins over the expanded card.
    expect(pickRedirectTarget(pending, "chk-2", "chk-1")?.checkpointId).toBe(
      "chk-2",
    );
    // A stale visible id falls back to the newest pending card.
    expect(
      pickRedirectTarget(pending, null, "chk-resolved")?.checkpointId,
    ).toBe("chk-3");
  });
});
