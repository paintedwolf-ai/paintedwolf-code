import { describe, expect, it } from "vitest";
import {
  DEVICE_TO_MIRROR_SECTION,
  MIRROR_TO_DEVICE_SECTION,
  isProjectSettingsMirrorSection,
} from "./project-settings-overlay-copy.ts";

describe("project settings overlay sections", () => {
  it("maps mirror sections to device Settings counterparts", () => {
    expect(MIRROR_TO_DEVICE_SECTION.providers).toBe("providers");
    expect(MIRROR_TO_DEVICE_SECTION.approvals).toBe("approvals");
    expect(MIRROR_TO_DEVICE_SECTION.mcp).toBe("mcp");
    expect(DEVICE_TO_MIRROR_SECTION.providers).toBe("providers");
    expect(isProjectSettingsMirrorSection("tests")).toBe(false);
  });
});
