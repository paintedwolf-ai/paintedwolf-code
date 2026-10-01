// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import { createShellStore } from "../../store/shell-store.ts";
import { createShellSearchNavigation } from "./shell-search-navigation.ts";

vi.mock("../../platform/connection/app-connection.ts", () => ({
  noticeReporterFor: () => ({ reportError: vi.fn() }),
}));
vi.mock("../../chat/transcript/presentation/transcript-reveal.ts", () => ({ revealChicklet: vi.fn(async () => true) }));

describe("shell search navigation", () => {
  it("waits for session restoration before revealing worker evidence", async () => {
    let finish!: () => void;
    const restoring = new Promise<void>((resolve) => { finish = resolve; });
    const shell = createShellStore("connected");
    const recent = { projectId: "project", sessionId: "session", title: "A chat" };
    const resumeSession = vi.fn(() => restoring);
    const openWorkers = vi.fn();
    const showProjects = vi.fn();
    const navigation = createShellSearchNavigation({
      shell, recents: { state: { recents: [recent] } } as never, closeSearch: vi.fn(), closeLauncher: vi.fn(),
      showProjects, openProject: vi.fn(), resumeSession, openWorkers,
    });
    const pending = navigation.navigateFromSearch({
      projectId: "project", sessionId: "session",
      reveal: { chicklet: "tool", anchorId: "tool", worker: { workerId: "worker", childSessionId: "child" } },
    });
    expect(resumeSession).toHaveBeenCalledWith(recent, expect.objectContaining({ stageSwitchDone: true }));
    expect(showProjects).toHaveBeenCalledOnce();
    expect(openWorkers).not.toHaveBeenCalled();
    finish();
    await pending;
    await Promise.resolve();
    expect(openWorkers).toHaveBeenCalledWith("worker", { scrollTo: "evidence" });
  });
});
