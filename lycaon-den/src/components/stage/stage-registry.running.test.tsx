import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { STAGE_REGISTRY, type StageRenderContext } from "./stage-registry.tsx";

vi.mock("../project/ProjectRunningView.tsx", () => ({
  ProjectRunningView: (p: { projectId: string; back: unknown; onOpenSession: (sessionId: string) => void }) => (
    <button
      data-testid="running-view"
      data-project={p.projectId}
      data-back={String(p.back)}
      onClick={() => p.onOpenSession("session-from-view")}
    />
  ),
}));

function context(projectId: string | null, onOpenSession = vi.fn()) {
  return {
    projectId,
    projectName: "",
    appStore: {},
    roots: [],
    projects: [],
    back: () => null,
    callbacks: { onOpenSession },
  } as unknown as StageRenderContext;
}

describe("running stage", () => {
  it("shows nothing without a project", () => {
    render(() => STAGE_REGISTRY.running.render(context(null)));
    expect(screen.queryByTestId("running-view")).toBeNull();
  });

  it("shows the project's running view and opens its sessions in that project", () => {
    const onOpenSession = vi.fn();
    render(() => STAGE_REGISTRY.running.render(context("project-1", onOpenSession)));
    const view = screen.getByTestId("running-view");
    expect(view.getAttribute("data-project")).toBe("project-1");
    expect(view.getAttribute("data-back")).toBe("null");
    fireEvent.click(view);
    expect(onOpenSession).toHaveBeenCalledWith({ projectId: "project-1", sessionId: "session-from-view" });
  });
});
