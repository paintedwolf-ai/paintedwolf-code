import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ProjectTrust } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { loadProjectTrust, openProjectTrust, projectTrustView, resetProjectTrustForTests, saveProjectTrust } from "./project-trust.ts";

const trust: ProjectTrust = { project_id: "project", unread_count: 0, surfaces: [], review: { id: "review", project_id: "project", changes: [] } };
beforeEach(resetProjectTrustForTests);
describe("project trust publication", () => {
  it("does not let an older read undo an acknowledgement", async () => {
    let finish!: (value: ProjectTrust) => void;
    const client = stubClient({ getProjectTrust: vi.fn(() => new Promise<ProjectTrust>(resolve => { finish = resolve; })), openProjectTrustReview: vi.fn(async () => trust) });
    const pending = loadProjectTrust(client, "project");
    await openProjectTrust(client, "project");
    finish({ ...trust, unread_count: 1 });
    await pending;
    expect(projectTrustView("project").value).toBe(trust);
  });
  it("does not publish a late response into a different project", async () => {
    let finish!: (value: ProjectTrust) => void;
    const second = { ...trust, project_id: "second" };
    const client = stubClient({ getProjectTrust: vi.fn((id: string) => id === "project" ? new Promise<ProjectTrust>(resolve => { finish = resolve; }) : Promise.resolve(second)) });
    const pending = loadProjectTrust(client, "project");
    await loadProjectTrust(client, "second");
    finish(trust); await pending;
    expect(projectTrustView("second").value).toBe(second);
    expect(projectTrustView("project").value).toBeNull();
  });
  it("does not let a delayed opening replace a newer acknowledgement", async () => {
    let finish!: (value: ProjectTrust) => void;
    const latest = { ...trust, review: { ...trust.review, id: "newer" } };
    const open = vi.fn<() => Promise<ProjectTrust>>()
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }))
      .mockResolvedValueOnce(latest);
    const client = stubClient({ openProjectTrustReview: open });
    const pending = openProjectTrust(client, "project");
    await openProjectTrust(client, "project");
    finish(trust); await pending;
    expect(projectTrustView("project").value).toBe(latest);
  });

  it("publishes a successful save after a background read failed", async () => {
    let finish!: (value: ProjectTrust) => void;
    const client = stubClient({
      updateProjectTrust: vi.fn(() => new Promise<ProjectTrust>(resolve => { finish = resolve; })),
      getProjectTrust: vi.fn().mockRejectedValue(new Error("disconnected")),
    });
    const pending = saveProjectTrust(client, "project", { enabled: { agents_md: false } });
    await expect(loadProjectTrust(client, "project")).rejects.toThrow("disconnected");
    expect(projectTrustView("project").value).toBeNull();
    finish(trust);
    await pending;
    expect(projectTrustView("project")).toMatchObject({ value: trust, error: null });
  });

  it("does not let an older opening undo a saved switch", async () => {
    let finish!: (value: ProjectTrust) => void;
    const saved = { ...trust, review: { ...trust.review, id: "saved" } };
    const client = stubClient({
      openProjectTrustReview: vi.fn(() => new Promise<ProjectTrust>(resolve => { finish = resolve; })),
      updateProjectTrust: vi.fn(async () => saved),
    });
    const pending = openProjectTrust(client, "project");
    await saveProjectTrust(client, "project", { enabled: { agents_md: false } });
    finish(trust);
    await pending;
    expect(projectTrustView("project").value).toBe(saved);
  });

  it("does not let a save restore an abandoned project", async () => {
    let finish!: (value: ProjectTrust) => void;
    const second = { ...trust, project_id: "second" };
    const client = stubClient({
      updateProjectTrust: vi.fn(() => new Promise<ProjectTrust>(resolve => { finish = resolve; })),
      getProjectTrust: vi.fn(async () => second),
    });
    const pending = saveProjectTrust(client, "project", { enabled: { agents_md: false } });
    await loadProjectTrust(client, "second");
    finish(trust);
    await pending;
    expect(projectTrustView("second").value).toBe(second);
    expect(projectTrustView("project").value).toBeNull();
  });

});
