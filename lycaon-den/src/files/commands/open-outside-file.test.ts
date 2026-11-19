import { afterEach, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { contributionCommandAvailable } from "../../contributions/dispatch.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { seedStockFrame } from "../../contributions/stock-frame-test.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { resetProjectFilesForTests } from "../documents/project-files-buffer-lifecycle.ts";
import { isOutsideRoot } from "../source/outside-namespace.ts";
import { openOutsideFile } from "./open-outside-file.ts";

const platform = vi.hoisted(() => ({
  desktop: vi.fn<() => boolean>(),
  platform: vi.fn<() => "macos" | "linux" | "windows">(),
  pick: vi.fn<() => Promise<{ path: string; grant: null } | null>>(),
}));
vi.mock("../../platform/runtime.ts", async importOriginal => ({
  ...await importOriginal<typeof import("../../platform/runtime.ts")>(),
  isTauriRuntime: platform.desktop,
  tauriPlatform: platform.platform,
}));
vi.mock("../../platform/files/native-path-dialog.ts", () => ({ nativePathDialog: platform.pick }));

afterEach(() => {
  resetContributionStoreForTest();
  resetProjectFilesForTests();
  vi.resetAllMocks();
});

it("offers outside opening only in Files when a chat and native picker are available", () => {
  const command = "painted-wolf/platform:files-open-outside-file";
  seedStockFrame();
  expect(contributionCommandAvailable(command)).toBe(true);
  for (const facts of [{ sessionExists: false }, { outsideFilePickerAvailable: false }, { filesStageActive: false }]) {
    seedStockFrame(facts);
    expect(contributionCommandAvailable(command)).toBe(false);
  }
});

it("uses the native-picked path and opens the returned file identity", async () => {
  const project = "picked-project";
  platform.desktop.mockReturnValue(true);
  platform.platform.mockReturnValue("linux");
  platform.pick.mockResolvedValue({ path: "/home/me/outside.txt", grant: null });
  const open = vi.fn<LycaonClient["openOutsideFile"]>().mockResolvedValue({
    root_id: "outside", path: "home/me/outside.txt", file_id: "file",
    location: {
      id: "location", session_id: "chat", root_id: "outside", anchor: "home/me", path: "/home/me",
      kind: "folder", label: "me", hidden: false, available: true,
    },
  });
  const client = stubClient({ openOutsideFile: open });
  await openOutsideFile(client, project, "chat");
  expect(open).toHaveBeenCalledExactlyOnceWith(project, { session_id: "chat", path: "/home/me/outside.txt" });
  const state = projectFilesState(project);
  expect(state.byKey[state.activeKey!]).toMatchObject({ rootId: "outside", path: "home/me/outside.txt", fileId: "file" });
  expect(isOutsideRoot(project, "outside")).toBe(true);
});

it("leaves the picker closed in the browser, on Windows, or without a chat", async () => {
  const open = vi.fn<LycaonClient["openOutsideFile"]>();
  const client = stubClient({ openOutsideFile: open });
  platform.desktop.mockReturnValue(false);
  platform.platform.mockReturnValue("linux");
  await openOutsideFile(client, "project", "chat");
  platform.desktop.mockReturnValue(true);
  platform.platform.mockReturnValue("windows");
  await openOutsideFile(client, "project", "chat");
  platform.platform.mockReturnValue("macos");
  await openOutsideFile(client, "project", undefined);
  expect(platform.pick).not.toHaveBeenCalled();
  expect(open).not.toHaveBeenCalled();
});
