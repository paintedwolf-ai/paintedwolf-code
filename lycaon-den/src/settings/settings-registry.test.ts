import { describe, expect, it } from "vitest";
import {
  SETTINGS_REGISTRY,
  findSettingDefinition,
  settingContext,
  settingsTabLabel,
  type SettingsSectionTabs,
} from "./settings-registry.ts";
import { APP_SETTINGS } from "./settings-nav-model.ts";

const TABBED: readonly (keyof SettingsSectionTabs)[] = [
  "general",
  "debug",
  "approvals",
  "web-research",
];

describe("settings registry", () => {
  it("gives every setting a unique id", () => {
    const ids = SETTINGS_REGISTRY.map((setting) => setting.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("places every setting in a Settings section, on a tab exactly when the section has tabs", () => {
    const sections = new Set<string>(APP_SETTINGS.map((item) => item.id));
    for (const setting of SETTINGS_REGISTRY) {
      expect(sections.has(setting.section), setting.id).toBe(true);
      const tabbed = (TABBED as readonly string[]).includes(setting.section);
      if (tabbed) {
        expect(setting.tab, setting.id).toBeDefined();
        expect(settingsTabLabel(setting.section, setting.tab ?? ""), setting.id).toBeDefined();
      } else {
        expect(setting.tab, setting.id).toBeUndefined();
      }
    }
  });

  it("keeps labels sentence case and keywords lowercase", () => {
    for (const setting of SETTINGS_REGISTRY) {
      expect(setting.label.trim(), setting.id).toBe(setting.label);
      expect(setting.label[0], setting.id).toBe(setting.label[0]?.toUpperCase());
      expect(setting.label.endsWith("."), setting.id).toBe(false);
      for (const keyword of setting.keywords) {
        expect(keyword, setting.id).toBe(keyword.trim().toLowerCase());
        expect(keyword.length, setting.id).toBeGreaterThan(0);
      }
      expect(new Set(setting.keywords).size, setting.id).toBe(setting.keywords.length);
    }
  });

  it("describes where a setting lives as section, tab, and group", () => {
    const fontSize = findSettingDefinition("editor-font-size");
    expect(fontSize && settingContext(fontSize)).toBe("General · Editor · Text");
    const theme = findSettingDefinition("appearance");
    expect(theme && settingContext(theme)).toBe("General · Display");
    const cost = findSettingDefinition("cost-tracking");
    expect(cost && settingContext(cost)).toBe("Cost");
    const neverAsk = findSettingDefinition("never-ask");
    expect(neverAsk && settingContext(neverAsk)).toBe("Advanced · Approvals");
  });

  it("resolves only registered ids", () => {
    expect(findSettingDefinition("text-size")?.label).toBe("Text size");
    expect(findSettingDefinition("not-a-setting")).toBeUndefined();
  });

  it("answers the words people type for common settings", () => {
    const matches = (query: string) =>
      SETTINGS_REGISTRY.filter((setting) =>
        [setting.label.toLowerCase(), ...setting.keywords].some((text) =>
          text.includes(query),
        ),
      ).map((setting) => setting.id);
    expect(matches("font size")).toEqual(
      expect.arrayContaining(["text-size", "editor-font-size"]),
    );
    expect(matches("dark mode")).toContain("appearance");
    expect(matches("api key")).toContain("ai-providers");
    expect(matches("notifications")).toContain("notifications");
  });
});
