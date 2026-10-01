import { describe, expect, it } from "vitest";
import {
  DEVICE_TO_MIRROR_SECTION,
  MIRROR_TO_DEVICE_SECTION,
  PROJECT_SETTINGS_OVERLAY_COPY,
  isProjectSettingsMirrorSection,
} from "./project-settings-overlay-copy.ts";
import { AI_PROVIDERS_SECTION_LABEL } from "../settings-nav-model.ts";

describe("project settings overlay copy", () => {
  it("uses a shared Project badge and Project configuration labels", () => {
    expect(PROJECT_SETTINGS_OVERLAY_COPY.badge).toBe("Project");
    expect(PROJECT_SETTINGS_OVERLAY_COPY.stageTitle).toBe("Project configuration");
    expect(PROJECT_SETTINGS_OVERLAY_COPY.overrideToggleLabel).toBe("Override");
  });

  it("maps mirror sections to device Settings counterparts", () => {
    expect(MIRROR_TO_DEVICE_SECTION.providers).toBe("providers");
    expect(MIRROR_TO_DEVICE_SECTION.approvals).toBe("approvals");
    expect(MIRROR_TO_DEVICE_SECTION.mcp).toBe("mcp");
    expect(DEVICE_TO_MIRROR_SECTION.providers).toBe("providers");
    expect(isProjectSettingsMirrorSection("tests")).toBe(false);
  });

  it("builds quiet Settings ↔ Project configuration cross-link labels", () => {
    expect(PROJECT_SETTINGS_OVERLAY_COPY.openInSettings("Approvals")).toBe(
      "Settings → Approvals",
    );
    expect(
      PROJECT_SETTINGS_OVERLAY_COPY.openInProjectSettings("MCP providers"),
    ).toBe("Project → MCP providers");
    expect(
      PROJECT_SETTINGS_OVERLAY_COPY.openInProjectSettingsNamed(
        "Alpha",
        AI_PROVIDERS_SECTION_LABEL,
      ),
    ).toBe(`Alpha → ${AI_PROVIDERS_SECTION_LABEL}`);
  });

  it("explains each direction before the counterpart jump", () => {
    expect(PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromSettings).toBe(
      "For this project only",
    );
    expect(PROJECT_SETTINGS_OVERLAY_COPY.counterpartHintFromProject).toBe(
      "Device-wide for every project",
    );
  });
});
