import { describe, expect, it } from "vitest";
import {
  ADVANCED_SETTINGS_TABS,
  AI_PROVIDERS_SECTION_LABEL,
  APP_SETTINGS,
  DEFAULT_ADVANCED_SETTINGS_TAB,
  DEFAULT_GENERAL_SETTINGS_TAB,
  DEFAULT_PROJECT_CONTEXT_SECTION,
  DEFAULT_SETTINGS_SECTION,
  GENERAL_SETTINGS_TABS,
  PROJECT_CONTEXT,
  isProjectContextSection,
} from "./settings-nav-model.ts";

describe("settings-nav-model", () => {
  it("splits app settings from project context sections", () => {
    expect(APP_SETTINGS.map((item) => item.id)).toEqual([
      "general",
      "providers",
      "approvals",
      "mcp",
      "scanners",
      "web-research",
      "extensions",
      "cost",
      "debug",
    ]);
    expect(PROJECT_CONTEXT.map((item) => item.id)).toEqual([
      "trust",
      "secrets",
      "providers",
      "approvals",
      "mcp",
      "scanners",
      "edit-review",
      "tests",
    ]);
    expect(APP_SETTINGS[0]?.label).toBe("General");
    expect(APP_SETTINGS[1]?.label).toBe(AI_PROVIDERS_SECTION_LABEL);
    expect(PROJECT_CONTEXT[0]?.label).toBe("Trust");
    expect(PROJECT_CONTEXT[1]?.label).toBe("Secrets");
    expect(PROJECT_CONTEXT[2]?.label).toBe(AI_PROVIDERS_SECTION_LABEL);
  });

  it("identifies project context sections", () => {
    expect(isProjectContextSection("trust")).toBe(true);
    expect(isProjectContextSection("secrets")).toBe(true);
    expect(isProjectContextSection("tests")).toBe(true);
    expect(isProjectContextSection("approvals")).toBe(true);
    expect(isProjectContextSection("mcp")).toBe(true);
    expect(isProjectContextSection("providers")).toBe(true);
    expect(isProjectContextSection("debug")).toBe(false);
    expect(isProjectContextSection("general")).toBe(false);
    expect(isProjectContextSection("web-research")).toBe(false);
  });

  it("defaults every fold to its own first row", () => {
    expect(DEFAULT_SETTINGS_SECTION).toBe(APP_SETTINGS[0]?.id);
    expect(DEFAULT_PROJECT_CONTEXT_SECTION).toBe(PROJECT_CONTEXT[0]?.id);
    expect(DEFAULT_GENERAL_SETTINGS_TAB).toBe(GENERAL_SETTINGS_TABS[0]?.id);
    expect(DEFAULT_ADVANCED_SETTINGS_TAB).toBe(ADVANCED_SETTINGS_TABS[0]?.id);
    expect(DEFAULT_SETTINGS_SECTION).toBe("general");
    expect(DEFAULT_PROJECT_CONTEXT_SECTION).toBe("trust");
  });

  it("places Cost between Extensions and Advanced", () => {
    const ids = APP_SETTINGS.map((item) => item.id);
    expect(ids.indexOf("extensions")).toBe(ids.indexOf("web-research") + 1);
    expect(ids.indexOf("cost")).toBe(ids.indexOf("extensions") + 1);
    expect(ids.indexOf("debug")).toBe(ids.indexOf("cost") + 1);
  });

  it("keeps Cost out of project configuration because it is a Context pane", () => {
    const ids = PROJECT_CONTEXT.map((item) => item.id);
    expect(ids).not.toContain("cost");
    expect(isProjectContextSection("cost")).toBe(false);
  });

  it("folds Revoke into Approvals; Scanners is top-level App Settings", () => {
    const ids = APP_SETTINGS.map((item) => item.id);
    expect(ids).not.toContain("revoke-approvals");
    expect(ids).toContain("scanners");
    expect(ids).not.toContain("host-resources");
    expect(ids).not.toContain("security");
    expect(ids).not.toContain("editor");
    expect(ids).not.toContain("keyboard");
    expect(ids).not.toContain("about");
  });

  it("tabs General with Display, Notifications, Editor, Keyboard, Power, Updates, About", () => {
    expect(GENERAL_SETTINGS_TABS.map((t) => t.id)).toEqual([
      "display",
      "notifications",
      "editor",
      "keyboard",
      "power",
      "updates",
      "about",
    ]);
    expect(DEFAULT_GENERAL_SETTINGS_TAB).toBe("display");
  });

  it("tabs Advanced with device trust controls", () => {
    expect(ADVANCED_SETTINGS_TABS.map((t) => t.id)).toEqual([
      "budgets",
      "approvals",
      "host_resources",
      "cache",
      "data",
      "diagnostics",
      "project_trust",
    ]);
    expect(ADVANCED_SETTINGS_TABS.find((t) => t.id === "data")?.label).toBe("Data");
    expect(ADVANCED_SETTINGS_TABS.find((t) => t.id === "approvals")?.label).toBe("Approvals");
    expect(ADVANCED_SETTINGS_TABS.find((t) => t.id === "project_trust")?.label).toBe(
      "Project trust",
    );
    expect(DEFAULT_ADVANCED_SETTINGS_TAB).toBe("budgets");
  });
});
