import { describe, expect, it } from "vitest";
import { wireProject } from "../api/mocks/project-fixture.ts";
import {
  resolveEditorSettingsScope,
  resolveGlobalSettingsScope,
  resolveProjectSettingsScope,
} from "./settings-scope.ts";

describe("settings scope", () => {
  it("resolves device settings globally", () => {
    expect(resolveGlobalSettingsScope()).toEqual({ scope: "global" });
    expect(resolveEditorSettingsScope(false, undefined, [])).toEqual({
      scope: "global",
    });
  });

  it("resolves project settings by registry path", () => {
    const projects = [wireProject("/tmp/project", "project-1")];
    expect(resolveProjectSettingsScope("/tmp/project/", projects)).toEqual({
      scope: "project",
      projectId: "project-1",
    });
  });

  it("rejects missing project context", () => {
    expect(() => resolveProjectSettingsScope(undefined, [])).toThrow(
      "Project settings require a project directory.",
    );
  });

  it("rejects unregistered project paths", () => {
    expect(() => resolveProjectSettingsScope("/tmp/missing", [])).toThrow(
      "Project settings cannot resolve /tmp/missing.",
    );
  });
});
