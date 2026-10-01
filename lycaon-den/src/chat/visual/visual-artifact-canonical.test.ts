import { describe, expect, it } from "vitest";
import type { Message } from "../../api/types.ts";
import {
  artifactFaceFromEntryKey,
  buildCanonicalArtifactPlacement,
  isCanonicalArtifactEntry,
  visualPresentEntryKey,
  visualProducerEntryKey,
} from "./visual-artifact-canonical.ts";

function msg(partial: Partial<Message> & Pick<Message, "id" | "role">): Message {
  const user = partial.role === "user";
  return {
    content: "",
    origin: user ? "user" : partial.role === "tool" ? "tool" : "model",
    authority: user ? "user" : "none",
    trust_tier: partial.role === "tool" ? "untrusted" : "trusted",
    created_at: "2026-07-14T15:00:00Z",
    ord: 0,
    ...partial,
  };
}

describe("buildCanonicalArtifactPlacement", () => {
  it("carries host-stamped pixel dimensions in visual metadata", () => {
    const placements = buildCanonicalArtifactPlacement([
      msg({
        id: "tool-1",
        role: "tool",
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-sized",
            mime: "image/png",
            source: "capture",
            width: 1280,
            height: 800,
          },
        },
      }),
    ]);
    expect(placements.meta("art-sized")).toMatchObject({ width: 1280, height: 800 });
  });

  it("lets producer and present each claim the same id", () => {
    const messages = [
      msg({
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          outcome: "error",
          visual: {
            id: "art-a",
            mime: "image/png",
            source: "capture",
            caption: "Hero",
          },
        },
      }),
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 2,
        artifact_ids: ["art-a"],
        content: "See figure",
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.site("producer", "art-a")).toMatchObject({
      kind: "producer",
      entryKey: visualProducerEntryKey("art-a"),
      caption: "Hero",
      source: "capture",
      mime: "image/png",
    });
    expect(placements.site("present", "art-a")).toMatchObject({
      kind: "present",
      entryKey: visualPresentEntryKey("asst-1", "art-a"),
      caption: "Hero",
      source: "capture",
      mime: "image/png",
    });
    expect(
      isCanonicalArtifactEntry(
        placements,
        "art-a",
        visualPresentEntryKey("asst-1", "art-a"),
      ),
    ).toBe(true);
    expect(
      isCanonicalArtifactEntry(placements, "art-a", visualProducerEntryKey("art-a")),
    ).toBe(true);
  });

  it("claims the first present when no producer precedes it", () => {
    const messages = [
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 1,
        artifact_ids: ["art-a"],
      }),
      msg({
        id: "asst-2",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 2,
        artifact_ids: ["art-a"],
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.site("present", "art-a")?.entryKey).toBe(
      visualPresentEntryKey("asst-1", "art-a"),
    );
    expect(
      isCanonicalArtifactEntry(
        placements,
        "art-a",
        visualPresentEntryKey("asst-2", "art-a"),
      ),
    ).toBe(false);
  });

  it("ignores internal rows so they cannot steal canonical placement", () => {
    const messages = [
      msg({
        id: "kick",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        ord: 1,
        visibility: "internal",
        artifact_ids: ["art-a"],
      }),
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 2,
        artifact_ids: ["art-a"],
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.site("present", "art-a")?.entryKey).toBe(
      visualPresentEntryKey("asst-1", "art-a"),
    );
  });

  it("harvests mime and caption from an internal producer for a later present", () => {
    const messages = [
      msg({
        id: "hidden-tool",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        visibility: "internal",
        tool_result: {
          content: "ok",
          visual: {
            id: "art-a",
            mime: "application/vnd.lycaon.filmstrip+zip",
            source: "capture",
            caption: "Drive",
          },
        },
      }),
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 2,
        artifact_ids: ["art-a"],
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.site("producer", "art-a")).toBeUndefined();
    expect(placements.meta("art-a")).toMatchObject({
      mime: "application/vnd.lycaon.filmstrip+zip",
      caption: "Drive",
      source: "capture",
    });
    expect(placements.site("present", "art-a")).toMatchObject({
      mime: "application/vnd.lycaon.filmstrip+zip",
      caption: "Drive",
    });
  });

  it("harvests worker-only visuals for parent present stubs", () => {
    const parent = [
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 1,
        artifact_ids: ["art-w"],
      }),
    ];
    const worker = [
      msg({
        id: "worker-tool",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-w",
            mime: "image/png",
            source: "capture",
            caption: "Worker shot",
          },
        },
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(parent, {
      metaMessages: worker,
    });
    expect(placements.site("producer", "art-w")).toBeUndefined();
    expect(placements.site("present", "art-w")).toMatchObject({
      caption: "Worker shot",
      mime: "image/png",
      source: "capture",
    });
  });

  it("keeps parent visual metadata when a worker row names the same id", () => {
    const parent = [
      msg({
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-a",
            mime: "image/png",
            source: "capture",
            caption: "Parent shot",
          },
        },
      }),
    ];
    const worker = [
      msg({
        id: "worker-tool",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-a",
            mime: "image/jpeg",
            source: "render",
            caption: "Worker shot",
          },
        },
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(parent, {
      metaMessages: worker,
    });
    expect(placements.meta("art-a")).toMatchObject({
      caption: "Parent shot",
      mime: "image/png",
      source: "capture",
    });
  });

  it("keys on artifact id, not caption — two ids with the same caption both claim", () => {
    const messages = [
      msg({
        id: "t1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-1",
            mime: "image/png",
            source: "render",
            caption: "Same",
          },
        },
      }),
      msg({
        id: "t2",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 2,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-2",
            mime: "image/png",
            source: "render",
            caption: "Same",
          },
        },
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.values()).toHaveLength(2);
    expect(placements.site("producer", "art-1")?.entryKey).toBe(
      visualProducerEntryKey("art-1"),
    );
    expect(placements.site("producer", "art-2")?.entryKey).toBe(
      visualProducerEntryKey("art-2"),
    );
  });

  it("preserves multi-id present order with independent claims", () => {
    const messages = [
      msg({
        id: "tool-1",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        ord: 1,
        tool_result: {
          content: "ok",
          visual: {
            id: "art-old",
            mime: "image/png",
            source: "capture",
            caption: "Old",
          },
        },
      }),
      msg({
        id: "asst-1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        ord: 2,
        artifact_ids: ["art-old", "art-new"],
      }),
    ];
    const placements = buildCanonicalArtifactPlacement(messages);
    expect(placements.site("producer", "art-old")?.kind).toBe("producer");
    expect(placements.site("present", "art-old")?.entryKey).toBe(
      visualPresentEntryKey("asst-1", "art-old"),
    );
    expect(placements.site("present", "art-new")?.entryKey).toBe(
      visualPresentEntryKey("asst-1", "art-new"),
    );
  });
});

describe("artifactFaceFromEntryKey", () => {
  it("classifies constructor keys and rejects unknown shapes", () => {
    expect(artifactFaceFromEntryKey(visualProducerEntryKey("art-a"), "art-a")).toBe(
      "producer",
    );
    expect(
      artifactFaceFromEntryKey(visualPresentEntryKey("asst-1", "art-a"), "art-a"),
    ).toBe("present");
    expect(artifactFaceFromEntryKey("visual:art-a", "art-other")).toBeNull();
    expect(artifactFaceFromEntryKey("random", "art-a")).toBeNull();
  });
});
