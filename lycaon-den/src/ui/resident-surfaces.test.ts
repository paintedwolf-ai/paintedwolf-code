import { createRoot, createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import {
  chatSessionKey,
  parseChatSessionKey,
  parseStageSurfaceKey,
  retainChatSurface,
  retainStageSurface,
  stageSurfaceKey,
  useResidentStack,
  SURFACE_HOME,
  SURFACE_SETTINGS,
  type ResidentStack,
} from "./resident-surfaces.ts";

function withStack(
  active: () => string | null,
  run: (stack: ResidentStack) => void,
  options?: Parameters<typeof useResidentStack>[1],
): void {
  let stack!: ResidentStack;
  const dispose = createRoot((d) => {
    stack = useResidentStack(active, options);
    return d;
  });
  try {
    run(stack);
  } finally {
    dispose();
  }
}

describe("useResidentStack", () => {
  it("seeds the first surface as active without overlap", () => {
    const [active, setActive] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      expect(stack.keys()).toEqual(["files"]);
      expect(stack.presence("files")).toBe("active");
      expect(stack.pending()).toBeNull();
      setActive("files");
      expect(stack.presence("files")).toBe("active");
    });
  });

  it("prepares a first visit before idling the outgoing surface", () => {
    const [active, setActive] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      setActive("search");
      expect(stack.keys()).toEqual(["files", "search"]);
      expect(stack.presence("files")).toBe("active");
      expect(stack.presence("search")).toBe("pending");
      stack.markReady("search");
      expect(stack.presence("files")).toBe("idle");
      expect(stack.presence("search")).toBe("active");
      expect(stack.pending()).toBeNull();
    });
  });

  it("swaps instantly when returning to a prepared surface", () => {
    const [active, setActive] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      stack.markReady("files");
      setActive("search");
      stack.markReady("search");
      setActive("files");
      expect(stack.presence("files")).toBe("active");
      expect(stack.presence("search")).toBe("idle");
      expect(stack.pending()).toBeNull();
    });
  });

  it("waits for the current payload generation when a suspended surface returns", () => {
    const [active, setActive] = createSignal<string | null>("first");
    const [generation, setGeneration] = createSignal(1);
    withStack(active, stack => {
      stack.markReady("first", 1);
      setActive("second");
      stack.markReady("second", 0);
      setGeneration(2);
      setActive("first");
      expect(stack.displayed()).toBe("second");
      expect(stack.pending()).toBe("first");
      stack.markReady("first", 1);
      expect(stack.displayed()).toBe("second");
      stack.markReady("first", 2);
      expect(stack.displayed()).toBe("first");
    }, { generation: key => key === "first" ? generation() : 0 });
  });

  it("prepares a revisited surface whose first load was interrupted", () => {
    const [active, setActive] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      setActive("search");
      stack.markReady("search");
      setActive("files");
      expect(stack.presence("search")).toBe("active");
      expect(stack.presence("files")).toBe("pending");
      stack.markReady("files");
      expect(stack.presence("files")).toBe("active");
    });
  });

  it("holds prepared surfaces until sibling preparation settles, including revisits", () => {
    const [active, setActive] = createSignal<string | null>("first");
    const [ready, setReady] = createSignal(true);
    withStack(active, (stack) => {
      stack.markReady("first");
      setReady(false);
      setActive("second");
      stack.markReady("second");
      expect(stack.displayed()).toBe("first");
      expect(stack.pending()).toBe("second");
      setReady(true);
      expect(stack.displayed()).toBe("second");
      setReady(false);
      setActive("first");
      expect(stack.displayed()).toBe("second");
      setReady(true);
      expect(stack.displayed()).toBe("first");
    }, { canPublish: ready });
  });

  it("rejects a superseded candidate while sibling preparation is pending", () => {
    const [active, setActive] = createSignal<string | null>("first");
    const [ready, setReady] = createSignal(true);
    withStack(active, (stack) => {
      stack.markReady("first");
      setReady(false);
      setActive("second");
      stack.markReady("second");
      setActive("third");
      setReady(true);
      expect(stack.displayed()).toBe("first");
      expect(stack.pending()).toBe("third");
      stack.markReady("third");
      expect(stack.displayed()).toBe("third");
    }, { canPublish: ready });
  });

  it("ignores markReady for a key that is not pending", () => {
    const [active] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      stack.markReady("files");
      expect(stack.presence("files")).toBe("active");
    });
  });

  it("keeps a pending surface behind the active surface", () => {
    const [active, setActive] = createSignal<string | null>("files");
    withStack(active, (stack) => {
      setActive("search");
      expect(stack.presence("search")).toBe("pending");
      expect(stack.presence("files")).toBe("active");
    });
  });

  it("evicts keys the retain predicate rejects", () => {
    const [active, setActive] = createSignal<string | null>("stage:p1:files");
    const [projectId, setProjectId] = createSignal("p1");
    withStack(
      active,
      (stack) => {
        setActive("stage:p1:search");
        stack.markReady("stage:p1:search");
        expect(stack.keys()).toEqual(["stage:p1:files", "stage:p1:search"]);
        setProjectId("p2");
        setActive("stage:p2:files");
        expect(stack.keys()).toEqual(["stage:p2:files"]);
        expect(stack.presence("stage:p2:files")).toBe("active");
      },
      { retain: (key) => retainStageSurface(key, projectId()) },
    );
  });

  it("keeps settings and home across project eviction", () => {
    const [active, setActive] = createSignal<string | null>(SURFACE_SETTINGS);
    const [projectId, setProjectId] = createSignal("p1");
    withStack(
      active,
      (stack) => {
        setActive(SURFACE_HOME);
        stack.markReady(SURFACE_HOME);
        setProjectId("p2");
        setActive("stage:p2:files");
        expect(stack.keys()).toEqual([
          SURFACE_SETTINGS,
          SURFACE_HOME,
          "stage:p2:files",
        ]);
      },
      { retain: (key) => retainStageSurface(key, projectId()) },
    );
  });
});

describe("surface keys", () => {
  it("round-trips stage and chat identities", () => {
    expect(parseStageSurfaceKey(stageSurfaceKey("p1", "files"))).toBe("files");
    expect(parseChatSessionKey(chatSessionKey("p1", "s1"))).toEqual({
      projectId: "p1",
      sessionId: "s1",
    });
    expect(retainChatSurface(chatSessionKey("p1", "s1"), "p1")).toBe(true);
    expect(retainChatSurface(chatSessionKey("p1", "s1"), "p2")).toBe(false);
    expect(retainStageSurface(stageSurfaceKey(null, "search"), null)).toBe(true);
    expect(retainStageSurface(stageSurfaceKey(null, "search"), "p1")).toBe(
      false,
    );
  });
});
