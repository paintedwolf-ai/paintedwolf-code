// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  confirmDestructive,
  setConfirmDestructivePresenter,
} from "./confirm-dialog.ts";

const here = dirname(fileURLToPath(import.meta.url));

describe("confirmDestructive", () => {
  afterEach(() => {
    setConfirmDestructivePresenter(null);
    vi.unstubAllGlobals();
  });

  it("asks window.confirm with the message when no host is mounted", async () => {
    const confirm = vi.fn(() => true);
    vi.stubGlobal("confirm", confirm);
    await expect(
      confirmDestructive({
        message: "Delete 3 blueprints?",
        title: "Delete blueprints",
        okLabel: "Delete 3",
      }),
    ).resolves.toBe(true);
    expect(confirm).toHaveBeenCalledWith("Delete 3 blueprints?");
  });

  it("carries a decline back to the caller", async () => {
    vi.stubGlobal("confirm", vi.fn(() => false));
    await expect(
      confirmDestructive({
        message: "Clear the index?",
        title: "Clear index",
        okLabel: "Clear",
      }),
    ).resolves.toBe(false);
  });

  it("uses the registered presenter instead of window.confirm", async () => {
    const confirm = vi.fn(() => true);
    vi.stubGlobal("confirm", confirm);
    setConfirmDestructivePresenter(async (req) => {
      expect(req.title).toBe("Delete chat");
      return false;
    });
    await expect(
      confirmDestructive({
        message: "Delete this chat and its full history? This cannot be undone.",
        title: "Delete chat",
        okLabel: "Delete",
      }),
    ).resolves.toBe(false);
    expect(confirm).not.toHaveBeenCalled();
  });

  it("does not open a native Tauri confirm sheet", () => {
    const src = readFileSync(join(here, "confirm-dialog.ts"), "utf8");
    expect(src).not.toMatch(/from ["']@tauri-apps\/plugin-dialog["']/);
    expect(src).not.toContain("import(\"@tauri-apps/plugin-dialog\")");
  });
});
