// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createShellPeerWindows } from "./shell-peer-windows.ts";

const openItemWindow = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("../../platform/windows/item-windows.ts", async (original) => ({
  ...(await original<typeof import("../../platform/windows/item-windows.ts")>()),
  openItemWindow,
  itemWindowViews: () => [],
}));
vi.mock("../../files/components/project-files-open.ts", () => ({ openSourceInFilesStage: vi.fn() }));

type Dependencies = Parameters<typeof createShellPeerWindows>[0];

function mount(itemSubject: Dependencies["itemSubject"]) {
  const shell = { state: { activeProjectId: "project" }, beginStageSwitch: vi.fn() };
  const openStage = vi.fn();
  const project = (id: string) => ({ id, name: "Garden", roots: [{ id: "root", path: "/repo", label: "repo" }] });
  const peers = createRoot(() => createShellPeerWindows({
    shell, appStore: { state: { sidecarStatus: "connected", currentSession: null } },
    projects: { byId: project }, activeProjectId: () => "project", activeProject: () => project("project"),
    stageAvailable: () => true, projectSessions: { state: { rows: [] } }, activeChat: () => null,
    itemSubject, stageColumnStageId: () => null, placeableStageId: () => null, splitLive: () => false, openStage,
  } as unknown as Dependencies));
  return { peers, shell, openStage };
}

describe("shell peer windows", () => {
  beforeEach(() => vi.clearAllMocks());

  it("opens a Context stage as its own addressed window", async () => {
    const { peers } = mount(null);
    await peers.detachStageToWindow("files");
    expect(openItemWindow).toHaveBeenCalledWith(expect.objectContaining({ kind: "context", projectId: "project", stageId: "files" }));
  });

  it("binds a pulled-off file's project before mounting its Files stage", async () => {
    const { shell, openStage } = mount({ kind: "file", projectId: "elsewhere", rootId: "root", path: "notes.md" } as Dependencies["itemSubject"]);
    await vi.waitFor(() => expect(openStage).toHaveBeenCalledWith("files"));
    expect(shell.beginStageSwitch).toHaveBeenCalledWith({ projectId: "elsewhere", kind: "cross-project" });
    expect(shell.beginStageSwitch.mock.invocationCallOrder[0]).toBeLessThan(openStage.mock.invocationCallOrder[0]!);
  });
});
