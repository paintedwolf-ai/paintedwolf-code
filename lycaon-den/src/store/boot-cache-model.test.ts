import { describe, expect, it } from "vitest";
import { wireProject } from "../api/mocks/project-fixture.ts";
import {
  cachedProjectsFromRegistry,
  cachedToProject,
  projectToCached,
} from "./boot-cache-model.ts";

describe("boot-cache-model", () => {
  it("round-trips project wire rows", () => {
    const project = wireProject("/tmp/a", "p1", "Alpha");
    const cached = projectToCached(project);
    expect(cachedToProject(cached).id).toBe("p1");
    expect(cachedToProject(cached).roots[0]?.path).toBe("/tmp/a");
  });

  it("keeps recent-first order capped at CACHED_PROJECTS_CAP", () => {
    const projects = Array.from({ length: 30 }, (_, i) =>
      wireProject(`/tmp/${i}`, `p${i}`, `P${i}`),
    );
    for (let i = 0; i < projects.length; i++) {
      projects[i]!.last_opened_at = new Date(2025, 0, i + 1).toISOString();
    }
    const cached = cachedProjectsFromRegistry(projects);
    expect(cached).toHaveLength(24);
    expect(cached[0]?.id).toBe("p29");
  });
});
