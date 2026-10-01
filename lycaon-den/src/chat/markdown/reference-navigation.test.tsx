import { createRoot, createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NavigationReference, NavigationTarget } from "../../api/types.ts";
import { buildProseNavigationIndex } from "./prose-path-opens.ts";
import { createReferenceNavigation } from "./reference-navigation.ts";
import { openSourceLocation, registerOpenSourceProjectLookup, registerOpenSourceSink, resetOpenSourceForTests } from "../../platform/navigation/open-source.ts";

const target: NavigationTarget = { project_id: "p", root_id: "r", path: "file.ts", entry_kind: "file" };
const resolved: NavigationReference = { id: "ref", syntax: "code", mention: "file.ts", explicit: true, status: "resolved", ...target };
const cleanups: (() => void)[] = [];
function controller(resolve: (id: string, index?: number) => Promise<NavigationReference | undefined>, known = resolved) {
  const [scope, setScope] = createSignal("message-1");
  const actions = createRoot((dispose) => {
    cleanups.push(dispose);
    return createReferenceNavigation({ scope, resolve, navigation: () => buildProseNavigationIndex([known]) });
  });
  return { ...actions, setScope };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("reference navigation", () => {
  const anchor = document.createElement("button");
  const sink = vi.fn();
  beforeEach(() => {
    resetOpenSourceForTests(); sink.mockReset();
    registerOpenSourceProjectLookup(() => ({ roots: [{ id: "r", path: "/repo" }] }));
    registerOpenSourceSink(sink);
  });
  afterEach(() => { for (const dispose of cleanups.splice(0)) dispose(); });

  it.each(["open", "reveal"] as const)("revalidates the candidate before applying %s", async (action) => {
    const resolve = vi.fn(async () => resolved);
    const actions = controller(resolve, { ...resolved, status: "ambiguous", candidates: [target] });
    await actions.activate(anchor, "ref", action);
    expect(resolve).not.toHaveBeenCalled();
    await actions.choose(target);
    expect(resolve).toHaveBeenCalledWith("ref", 0);
    expect(sink).toHaveBeenCalledWith(expect.objectContaining({ path: "file.ts", ...(action === "reveal" ? { action: "reveal" } : { intent: "permanent" }) }));
    expect(actions.displayedChooser()).toBeUndefined();
  });

  it("does not open an earlier message's link after a newer navigation", async () => {
    const pending = deferred<NavigationReference>();
    const first = controller(() => pending.promise);
    const started = first.activate(anchor, "ref", "open");
    await openSourceLocation({ projectId: "p", path: "newer.ts", intent: "permanent" });
    pending.resolve(resolved);
    await started;
    expect(sink).toHaveBeenCalledTimes(1);
    expect(sink).toHaveBeenLastCalledWith(expect.objectContaining({ path: "newer.ts" }));
    expect(first.displayedChooser()).toBeUndefined();
  });

  it("ignores completion after the source message changes", async () => {
    const pending = deferred<NavigationReference>();
    const actions = controller(() => pending.promise);
    const started = actions.activate(anchor, "ref", "reveal");
    actions.setScope("message-2");
    pending.resolve(resolved);
    await started;
    expect(sink).not.toHaveBeenCalled();
  });

  it("keeps unavailable results in the chooser without fabricating a destination", async () => {
    const actions = controller(async () => ({ ...resolved, status: "missing" }));
    await actions.activate(anchor, "ref", "reveal");
    expect(actions.displayedChooser()?.reference?.status).toBe("missing");
    expect(sink).not.toHaveBeenCalled();
  });
});
