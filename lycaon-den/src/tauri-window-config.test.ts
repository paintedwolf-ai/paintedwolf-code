import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const tauriRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "src-tauri");

type MainWindowConfig = {
  decorations?: boolean;
  titleBarStyle?: string;
};

function mainWindow(file: string): MainWindowConfig {
  const parsed = JSON.parse(readFileSync(join(tauriRoot, file), "utf8")) as {
    app?: { windows?: MainWindowConfig[] };
  };
  const window = parsed.app?.windows?.[0];
  if (!window) expect.fail(`missing main window in ${file}`);
  return window;
}

describe("tauri platform window config", () => {
  it("macOS overlay keeps decorations so traffic lights render", () => {
    expect(mainWindow("tauri.macos.conf.json")).toMatchObject({
      decorations: true,
      titleBarStyle: "Overlay",
    });
  });

  it("Linux uses undecorated window with in-app controls", () => {
    expect(mainWindow("tauri.linux.conf.json").decorations).toBe(false);
  });
});
