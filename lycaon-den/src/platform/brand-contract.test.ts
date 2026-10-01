import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  BUNDLE_IDENTIFIER,
  CONFIG_DIR_LABEL,
  CONFIG_DIR_LABEL_DEV,
  CONFIG_DIR_NAME,
  CONFIG_DIR_NAME_DEV,
  MAIN_BINARY_NAME,
  PRODUCT_NAME,
} from "../../shared/brand.ts";

const root = join(dirname(fileURLToPath(import.meta.url)), "../..");

function read(rel: string): string {
  return readFileSync(join(root, rel), "utf8");
}

describe("product branding", () => {
  it("user-visible copy uses Painted Wolf Code, not engine codenames", () => {
    const literalPaths = [
      "index.html",
      "src-tauri/tauri.conf.json",
    ];
    for (const rel of literalPaths) {
      const text = read(rel);
      expect(text).toContain("Painted Wolf Code");
      expect(text.toLowerCase()).not.toMatch(/\blycaon den\b/);
    }
    expect(PRODUCT_NAME).toBe("Painted Wolf Code");
  });

  it("user-visible copy never says Wild Dog", () => {
    const userVisiblePaths = [
      "index.html",
      "src/components/shell/Shell.tsx",
      "src-tauri/tauri.conf.json",
    ];
    for (const rel of userVisiblePaths) {
      expect(read(rel).toLowerCase()).not.toMatch(/\bwild dog\b/);
    }
  });

  it("checkpoint UI uses Edit review product copy", () => {
    const paths = [
      "src/settings/security/approvals-copy.ts",
      "src/components/settings/appearance/EditReviewSettingsPanel.tsx",
    ];
    for (const rel of paths) {
      expect(read(rel)).toContain("Edit review");
      expect(read(rel).toLowerCase()).not.toMatch(/\blycaon\b/);
    }
    const forbidden = [/permission-decisions/i, /diff-reviews/i];
    for (const rel of [
      "src/components/checkpoint/ApprovalCard.tsx",
      "src/components/checkpoint/CheckpointCards.tsx",
      "src/components/chatview/ChatView.tsx",
      "src/api/client.ts",
    ]) {
      const text = read(rel);
      for (const pattern of forbidden) {
        expect(text).not.toMatch(pattern);
      }
    }
  });

  it("bundle id and binary stay painted-wolf; config dir is paintedwolf", () => {
    const tauriConf = read("src-tauri/tauri.conf.json");
    expect(tauriConf).toContain(`"identifier": "${BUNDLE_IDENTIFIER}"`);
    expect(tauriConf).toContain(`"mainBinaryName": "${MAIN_BINARY_NAME}"`);
    expect(tauriConf).toContain('"csp": "default-src');
    expect(tauriConf).not.toContain('"csp": null');
    expect(tauriConf.toLowerCase()).not.toContain("lycaon");

    expect(BUNDLE_IDENTIFIER).toBe("dev.paintedwolf.code");
    expect(MAIN_BINARY_NAME).toBe("painted-wolf-code");
    expect(CONFIG_DIR_NAME).toBe("paintedwolf");
    expect(CONFIG_DIR_NAME_DEV).toBe("paintedwolf-dev");
    expect(CONFIG_DIR_LABEL).toBe("~/.config/paintedwolf");
    expect(CONFIG_DIR_LABEL_DEV).toBe("~/.config/paintedwolf-dev");

    expect(read("src-tauri/src/lib.rs").toLowerCase()).not.toContain("lycaon-den");
    expect(read("src-tauri/src/lib.rs")).toContain("mod config_dir;");
    expect(read("src-tauri/src/config_dir.rs")).toContain(
      `DIR_NAME_PROD: &str = "${CONFIG_DIR_NAME}"`,
    );
    expect(read("src-tauri/src/config_dir.rs")).toContain(
      `DIR_NAME_DEV: &str = "${CONFIG_DIR_NAME_DEV}"`,
    );
    expect(read("src-tauri/src/sidecar.rs")).toContain("LYCAON_CONFIG_DIR");
    expect(read("src-tauri/src/sidecar.rs")).not.toContain('.join("lycaon")');
  });
});
