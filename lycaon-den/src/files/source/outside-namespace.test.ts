import { describe, expect, it } from "vitest";
import { isOutsideRoot, outsideHostDirectory, outsideHostPath, outsideRootId, recordOutsideRoot } from "./outside-namespace.ts";

describe("outside namespace", () => {
  it("knows each project's outside root separately", () => {
    recordOutsideRoot("project-a", "outside-a");
    recordOutsideRoot("project-b", "outside-b");
    expect(outsideRootId("project-a")).toBe("outside-a");
    expect(isOutsideRoot("project-a", "outside-a")).toBe(true);
    expect(isOutsideRoot("project-a", "outside-b")).toBe(false);
    expect(isOutsideRoot("project-c", undefined)).toBe(false);
  });

  it("spells outside paths as host paths", () => {
    expect(outsideHostPath("Users/me/.zshrc")).toBe("/Users/me/.zshrc");
    expect(outsideHostDirectory("Users/me/.zshrc")).toBe("/Users/me");
    expect(outsideHostDirectory("hosts")).toBe("/");
  });
});
