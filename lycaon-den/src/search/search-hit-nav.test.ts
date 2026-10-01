import { describe, expect, it } from "vitest";
import type { SearchHit } from "../api/types.ts";
import { searchHitToNavTarget } from "./search-hit-nav.ts";

function hit(partial: Partial<SearchHit> & Pick<SearchHit, "hit_kind">): SearchHit {
  return {
    hit_id: `test:${partial.hit_kind}`,
    source: "store",
    project_id: "p1",
    ...partial,
    title: partial.title ?? partial.hit_kind,
  };
}

describe("searchHitToNavTarget", () => {
  it("sends a message hit to the message, not its evidence chicklet", () => {
    const target = searchHitToNavTarget(
      hit({ hit_kind: "message", session_id: "s1", source_ref: "msg-9" }),
      "retry budget",
    );
    expect(target.sessionId).toBe("s1");
    expect(target.reveal).toMatchObject({
      anchorId: "msg-9",
      chicklet: "message",
      matchText: "retry budget",
    });
  });

  it("keeps evidence and claim hits on the citation chicklet", () => {
    for (const kind of ["evidence", "claim"]) {
      const target = searchHitToNavTarget(
        hit({ hit_kind: kind, session_id: "s1", source_ref: "msg-3" }),
      );
      expect(target.reveal?.chicklet, kind).toBe("citation");
    }
  });

  it("keeps tool-shaped hits on the tool card", () => {
    for (const kind of ["tool", "web", "network"]) {
      const target = searchHitToNavTarget(
        hit({ hit_kind: kind, session_id: "s1", source_ref: "call-2" }),
      );
      expect(target.reveal?.chicklet, kind).toBe("tool");
    }
  });

  it("routes a worker hit to the parent chat and remembers the worker", () => {
    const target = searchHitToNavTarget(
      hit({
        hit_kind: "message",
        session_id: "child-1",
        parent_session_id: "parent-1",
        worker_id: "w-1",
        source_ref: "msg-4",
      }),
    );
    expect(target.sessionId).toBe("parent-1");
    expect(target.reveal?.worker).toEqual({
      workerId: "w-1",
      childSessionId: "child-1",
    });
  });

  it("requires complete worker coordinates", () => {
    const target = searchHitToNavTarget(
      hit({
        hit_kind: "message",
        session_id: "child-1",
        parent_session_id: "parent-1",
        source_ref: "msg-4",
      }),
    );
    expect(target.sessionId).toBe("child-1");
    expect(target.reveal?.worker).toBeUndefined();
  });

  it("file and code hits carry no transcript reveal", () => {
    for (const kind of ["file", "code"]) {
      const target = searchHitToNavTarget(
        hit({ hit_kind: kind, path: "a.ts", source_ref: "a.ts" }),
      );
      expect(target.reveal, kind).toBeUndefined();
    }
  });
});
