import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRoot, createSignal } from "solid-js";
import type { AgentSessionPresence } from "../../api/types.ts";
import { resetEditorPrefsForTests } from "../../settings/editor/editor-prefs.ts";
import { AGENT_LINGER_MS } from "./agent-presence.ts";
import { applyAgentPresenceEvent, resetAgentPresenceForTests } from "./agent-presence-store.ts";
import { createFilesAgentPresence, type FilesAgentPresence } from "./files-agent-presence.ts";

let revision = 0;

function publish(project: string, presence: Partial<AgentSessionPresence>): void {
  applyAgentPresenceEvent({
    project_id: project,
    session_id: "s1",
    revision: ++revision,
    presence: { session_id: "s1", title: "Fix login", turn: 1, activities: [], reads: [], intents: [], worker_drafts: [], ...presence },
  });
}

const editing = { tool_call_id: "c1", tool: "edit", root_id: "r1", path: "a.ts", kind: "editing" as const };
const reading = { tool_call_id: "c2", tool: "read", root_id: "r1", path: "a.ts", kind: "reading" as const };
const read = {
  id: "read-1", sequence: 1, tool_call_id: "c2", tool: "read", root_id: "r1", path: "a.ts",
  extent: "whole_file" as const, ranges: [], stale: false,
};
const waiting = {
  id: "i1", tool_call_id: "c3", tool: "edit", operation: "edit" as const, state: "awaiting_approval" as const,
  root_id: "r1", path: "a.ts", extent: "range" as const, ranges: [{ start_line: 1, end_line: 2 }],
};

function mount() {
  const [project, setProject] = createSignal("p1");
  let presenceFor!: (rootId: string, path: string) => FilesAgentPresence | null;
  const dispose = createRoot((dispose) => {
    presenceFor = createFilesAgentPresence({ projectId: project, openSessionId: () => undefined });
    return dispose;
  });
  return { file: () => presenceFor("r1", "a.ts"), setProject, dispose };
}

describe("files agent presence", () => {
  beforeEach(() => {
    vi.useFakeTimers({ now: 0 });
    resetAgentPresenceForTests();
    resetEditorPrefsForTests();
  });

  afterEach(() => {
    resetAgentPresenceForTests();
    resetEditorPrefsForTests();
    vi.useRealTimers();
  });

  it("keeps what live editing showed, marked past, until the bound", () => {
    const view = mount();
    publish("p1", { activities: [editing] });
    const live = view.file()!;
    expect(live.chip).toMatchObject({ state: "editing", phase: "live" });
    vi.advanceTimersByTime(30_000);

    publish("p1", {});
    const past = view.file()!;
    expect(past.chip).toEqual({ ...live.chip, phase: "past" });
    expect(past.name).toEqual({ ...live.name, phase: "past" });
    expect(past.labels).toEqual(live.labels);

    vi.advanceTimersByTime(AGENT_LINGER_MS - 1);
    expect(view.file()?.chip?.phase).toBe("past");
    vi.advanceTimersByTime(1);
    expect(view.file()).toBeNull();
    view.dispose();
  });

  it("keeps a live name while the editing chip goes past, and lists the ended fact with the live ones", () => {
    const view = mount();
    publish("p1", { activities: [editing], reads: [read] });
    const editingFact = view.file()!.chip!.description;
    publish("p1", { reads: [read] });

    const file = view.file()!;
    expect(file.chip).toMatchObject({ phase: "past", description: editingFact });
    expect(file.name?.phase).toBe("live");
    expect(file.labels).toEqual([editingFact, ...file.name!.labels]);
    expect(file.name!.labels).not.toContain(editingFact);
    view.dispose();
  });

  it("changes state in place while live and lingers only the last state", () => {
    const view = mount();
    publish("p1", { activities: [reading] });
    publish("p1", { activities: [editing] });
    expect(view.file()?.chip).toMatchObject({ state: "editing", phase: "live" });

    publish("p1", {});
    expect(view.file()?.chip).toMatchObject({ state: "editing", phase: "past" });
    view.dispose();
  });

  it("removes a waiting chip in the update that ends it", () => {
    const view = mount();
    publish("p1", { intents: [waiting] });
    expect(view.file()?.chip?.state).toBe("waiting");

    publish("p1", {});
    expect(view.file()).toBeNull();
    view.dispose();
  });

  it("drops lingering activity at once when a person turns file names off", () => {
    const view = mount();
    publish("p1", { activities: [editing] });
    publish("p1", {});
    expect(view.file()?.chip?.phase).toBe("past");

    resetEditorPrefsForTests({ agentActivityKinds: { fileNames: false } });
    expect(view.file()).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
    view.dispose();
  });

  it("keeps live activity of a kind still shown when another kind is turned off", () => {
    const view = mount();
    publish("p1", { activities: [editing] });

    resetEditorPrefsForTests({ agentActivityKinds: { reads: false } });
    expect(view.file()?.chip).toMatchObject({ state: "editing", phase: "live" });
    view.dispose();
  });

  it("drops lingering activity when the project changes", () => {
    const view = mount();
    publish("p1", { activities: [editing] });
    publish("p1", {});

    view.setProject("p2");
    expect(view.file()).toBeNull();
    view.dispose();
  });
});
