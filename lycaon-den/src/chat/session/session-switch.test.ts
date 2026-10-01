import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { describe, expect, it, vi } from "vitest";
import {
  createSessionSwitchGeneration,
  runCreateSession,
  runResumeSession,
  type SessionSwitchDeps,
} from "./session-switch.ts";
import { SESSION_CREATE_PENDING_ID } from "./session-scope.ts";

function makeDeps(): SessionSwitchDeps & { calls: string[] } {
  const calls: string[] = [];
  return {
    calls,
    returnToHome: () => calls.push("returnToHome"),
    clearChatForSessionSwitch: (lock) => calls.push(`clearChat:${lock ?? ""}`),
    beginSessionResumeSwitch: (lock) => calls.push(`resumeSwitch:${lock ?? ""}`),
    completeChatSessionHydration: () => calls.push("complete"),
    resetChat: () => calls.push("resetChat"),
    reportError: (err, scope) =>
      calls.push(
        `report:${scope.kind}:${err instanceof Error ? err.message : String(err)}`,
      ),
    openSession: (scope) => calls.push(`open:${scope.projectId}:${scope.sessionId}`),
    setConnected: () => calls.push("connected"),
    snapshotSessionChat: () => calls.push("snapshot"),
    restoreCachedSessionChat: (scope) => {
      calls.push(`restore:${scope.projectId}:${scope.sessionId}`);
      return false;
    },
    prepareProject: async (projectId) => {
      calls.push(`prepare:${projectId}`);
    },
  };
}

describe("runResumeSession", () => {
  it("same-project restores cache, resume-switches, opens, prepares, then hydrates", async () => {
    const deps = makeDeps();
    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "s1" },
      deps,
      hasClient: true,
      hydrate: async () => {
        deps.calls.push("hydrate");
      },
    });
    expect(deps.calls).toEqual([
      "snapshot",
      "restore:p1:s1",
      "resumeSwitch:s1",
      "open:p1:s1",
      "prepare:p1",
      "hydrate",
      "complete",
      "connected",
    ]);
  });

  it("moves a same-project selection before project preparation settles", async () => {
    const deps = makeDeps();
    const generation = createSessionSwitchGeneration();
    const releases: Array<() => void> = [];
    deps.prepareProject = (projectId) => {
      deps.calls.push(`prepare:${projectId}`);
      return new Promise<void>((resolve) => releases.push(resolve));
    };
    const hydrate = vi.fn(async () => undefined);

    const first = runResumeSession({
      generation, kind: "same-project", scope: { projectId: "p1", sessionId: "s1" },
      deps, hasClient: true, hydrate,
    });
    const second = runResumeSession({
      generation, kind: "same-project", scope: { projectId: "p1", sessionId: "s2" },
      deps, hasClient: true, hydrate,
    });

    // Both selections land while the event stream is still reconnecting.
    expect(deps.calls.filter((c) => c.startsWith("open:"))).toEqual([
      "open:p1:s1",
      "open:p1:s2",
    ]);
    for (const release of releases) release();
    await Promise.all([first, second]);
    expect(hydrate).toHaveBeenCalledTimes(1);
    expect(deps.calls.filter((c) => c === "complete")).toHaveLength(1);
  });

  it("returns Home when same-project preparation fails after the selection moved", async () => {
    const deps = makeDeps();
    deps.prepareProject = async () => {
      deps.calls.push("prepare:p1");
      throw new Error("subscription failed");
    };
    const hydrate = vi.fn();

    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "s1" },
      deps,
      hasClient: true,
      hydrate,
    });

    expect(hydrate).not.toHaveBeenCalled();
    expect(deps.calls.slice(-4)).toEqual([
      "prepare:p1",
      "resetChat",
      "returnToHome",
      "report:session:subscription failed",
    ]);
  });

  it("aborts a superseded switch's reads when a newer switch begins", async () => {
    const deps = makeDeps();
    const generation = createSessionSwitchGeneration();
    let firstSignal: AbortSignal | undefined;
    let entered!: () => void;
    const started = new Promise<void>((resolve) => {
      entered = resolve;
    });

    const stale = runResumeSession({
      generation,
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "s1" },
      deps,
      hasClient: true,
      hydrate: ({ signal }) => {
        firstSignal = signal;
        entered();
        return new Promise<void>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(signal.reason as Error), { once: true });
        });
      },
    });
    await started;
    expect(firstSignal?.aborted).toBe(false);

    let secondSignal: AbortSignal | undefined;
    await runResumeSession({
      generation,
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "s2" },
      deps,
      hasClient: true,
      hydrate: async ({ signal }) => {
        secondSignal = signal;
      },
    });
    await stale;

    expect(firstSignal?.aborted).toBe(true);
    expect(secondSignal?.aborted).toBe(false);
    // The aborted read is a superseded switch, not a failure to report.
    expect(deps.calls.some((c) => c.startsWith("report:"))).toBe(false);
    expect(deps.calls).not.toContain("returnToHome");
  });

  it("passes shouldApply into hydrate and drops stale completion", async () => {
    const deps = makeDeps();
    const gen = createSessionSwitchGeneration();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let hydrateEntered!: () => void;
    const entered = new Promise<void>((resolve) => {
      hydrateEntered = resolve;
    });
    let sawShouldApply = false;

    const stale = runResumeSession({
      generation: gen,
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "slow" },
      deps,
      hasClient: true,
      hydrate: async ({ shouldApply }) => {
        sawShouldApply = typeof shouldApply === "function";
        hydrateEntered();
        await gate;
        expect(shouldApply()).toBe(false);
      },
    });
    await entered;
    await runResumeSession({
      generation: gen,
      kind: "same-project",
      scope: { projectId: "p1", sessionId: "fast" },
      deps,
      hasClient: true,
      hydrate: async () => {
        deps.calls.push("hydrate-fast");
      },
    });
    release();
    await stale;
    expect(sawShouldApply).toBe(true);
    expect(deps.calls.filter((c) => c === "complete")).toHaveLength(1);
    expect(deps.calls).toContain("hydrate-fast");
    expect(deps.calls).toContain("open:p1:fast");
  });

  it("cross-project clears chat before open, prepare, and hydrate", async () => {
    const deps = makeDeps();
    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "cross-project",
      scope: { projectId: "p2", sessionId: "s2" },
      deps,
      hasClient: true,
      hydrate: async () => {
        deps.calls.push("hydrate");
      },
    });
    expect(deps.calls).toEqual([
      "snapshot",
      "clearChat:*",
      "open:p2:s2",
      "prepare:p2",
      "hydrate",
      "complete",
      "connected",
    ]);
    expect(deps.calls).not.toContain("resumeSwitch:s2");
    expect(deps.calls.some((c) => c.startsWith("restore:"))).toBe(false);
  });

  it("cross-project never calls beginSessionResumeSwitch", async () => {
    const deps = makeDeps();
    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "cross-project",
      scope: { projectId: "p2", sessionId: "s2" },
      deps,
      hasClient: true,
      hydrate: async () => undefined,
    });
    expect(deps.calls.some((c) => c.startsWith("resumeSwitch:"))).toBe(false);
  });

  it("recovers when project preparation fails before hydration", async () => {
    const deps = makeDeps();
    deps.prepareProject = async () => {
      deps.calls.push("prepare:p2");
      throw new Error("subscription failed");
    };

    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "cross-project",
      scope: { projectId: "p2", sessionId: "s2" },
      deps,
      hasClient: true,
      hydrate: vi.fn(),
    });

    expect(deps.calls).toEqual([
      "snapshot",
      "clearChat:*",
      "open:p2:s2",
      "prepare:p2",
      "resetChat",
      "returnToHome",
      "report:session:subscription failed",
    ]);
  });
});

describe("runCreateSession", () => {
  it("unlocks the chat stage before deferred enrich", async () => {
    const deps = makeDeps();
    let releaseEnrich!: () => void;
    const enrichGate = new Promise<void>((resolve) => {
      releaseEnrich = resolve;
    });
    let enrichStarted!: () => void;
    const enrichEntered = new Promise<void>((resolve) => {
      enrichStarted = resolve;
    });

    const created = runCreateSession({
      generation: createSessionSwitchGeneration(),
      projectId: "p1",
      deps,
      hasClient: true,
      create: async () => ({ projectId: "p1", sessionId: "s2" }),
      enrich: async () => {
        deps.calls.push("enrich");
        enrichStarted();
        await enrichGate;
      },
    });

    await enrichEntered;
    // The chat stage unlocks independently.
    expect(deps.calls).toEqual([
      "clearChat:*",
      `open:p1:${SESSION_CREATE_PENDING_ID}`,
      "open:p1:s2",
      "prepare:p1",
      "complete",
      "connected",
      "enrich",
    ]);
    const result = await created;
    expect(result?.scope).toEqual({ projectId: "p1", sessionId: "s2" });
    releaseEnrich();
    await result?.enrichment;
  });

  it("runs project follow-up only after bootstrap enrichment settles", async () => {
    const deps = makeDeps();
    let releaseEnrich!: () => void;
    const enrichGate = new Promise<void>((resolve) => {
      releaseEnrich = resolve;
    });
    const created = await runCreateSession({
      generation: createSessionSwitchGeneration(),
      projectId: "p1",
      deps,
      hasClient: true,
      create: async () => ({ projectId: "p1", sessionId: "s2" }),
      enrich: async () => {
        deps.calls.push("enrich");
        await enrichGate;
        deps.calls.push("enriched");
      },
      afterEnrich: async () => {
        deps.calls.push("afterEnrich");
      },
    });

    await Promise.resolve();
    expect(deps.calls).not.toContain("afterEnrich");
    releaseEnrich();
    await created?.enrichment;
    await vi.waitFor(() => expect(deps.calls).toContain("afterEnrich"));
    expect(deps.calls.indexOf("afterEnrich")).toBeGreaterThan(
      deps.calls.indexOf("enriched"),
    );
  });

  it("passes navigation validity into an in-flight create", async () => {
    const deps = makeDeps();
    const generation = createSessionSwitchGeneration();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    let entered!: () => void;
    const started = new Promise<void>((resolve) => {
      entered = resolve;
    });

    const creating = runCreateSession({
      generation,
      projectId: "p1",
      deps,
      hasClient: true,
      create: async ({ shouldApply }) => {
        expect(shouldApply()).toBe(true);
        entered();
        await gate;
        expect(shouldApply()).toBe(false);
        return { projectId: "p1", sessionId: "s2" };
      },
    });

    await started;
    generation.next();
    release();

    await expect(creating).resolves.toBeNull();
    expect(deps.calls).toEqual([
      "clearChat:*",
      `open:p1:${SESSION_CREATE_PENDING_ID}`,
    ]);
  });

  it("reports when offline", async () => {
    const deps = makeDeps();
    await runCreateSession({
      generation: createSessionSwitchGeneration(),
      projectId: "p1",
      deps,
      hasClient: false,
      create: vi.fn(),
    });
    expect(deps.calls).toEqual([
      `report:app:${CLIENT_NOTICES.offline.message}`,
    ]);
  });

  it("returns Home when new-session preparation fails", async () => {
    const deps = makeDeps();
    deps.prepareProject = async () => {
      deps.calls.push("prepare:p1");
      throw new Error("subscription failed");
    };

    const created = await runCreateSession({
      generation: createSessionSwitchGeneration(),
      projectId: "p1",
      deps,
      hasClient: true,
      create: async () => ({ projectId: "p1", sessionId: "s2" }),
      enrich: vi.fn(),
    });

    expect(created).toBeNull();
    expect(deps.calls).toEqual([
      "clearChat:*",
      `open:p1:${SESSION_CREATE_PENDING_ID}`,
      "open:p1:s2",
      "prepare:p1",
      "resetChat",
      "returnToHome",
      "report:app:subscription failed",
    ]);
    expect(deps.calls).not.toContain("complete");
  });
});
