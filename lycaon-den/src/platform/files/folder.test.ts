import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../runtime.ts", () => ({ isTauriRuntime: vi.fn() }));
vi.mock("./native-path-dialog.ts", () => ({ nativePathDialog: vi.fn() }));
vi.mock("../connection/host-identity.ts", () => ({ hostSharesDevice: vi.fn() }));
vi.mock("./host-folder-dialog.ts", () => ({ requestHostFolder: vi.fn() }));

import { pickProjectFolder } from "./folder.ts";
import { nativePathDialog } from "./native-path-dialog.ts";
import { isTauriRuntime } from "../runtime.ts";
import { hostSharesDevice } from "../connection/host-identity.ts";
import { requestHostFolder } from "./host-folder-dialog.ts";

const runtime = vi.mocked(isTauriRuntime);
const dialog = vi.mocked(nativePathDialog);

describe("pickProjectFolder", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    runtime.mockReturnValue(true);
    vi.mocked(hostSharesDevice).mockReturnValue(true);
  });

  it("propagates a native picker failure instead of reporting a false cancellation", async () => {
    const failure = new Error("native picker unavailable");
    dialog.mockRejectedValue(failure);

    await expect(pickProjectFolder()).rejects.toThrow(failure);
  });

  it("keeps an explicit cancellation distinct from a picker failure", async () => {
    dialog.mockResolvedValue(null);

    await expect(pickProjectFolder()).resolves.toBeNull();
  });

  it("returns the chosen folder path", async () => {
    dialog.mockResolvedValue({ path: "/Users/x/projects/app", grant: null });

    await expect(pickProjectFolder()).resolves.toBe("/Users/x/projects/app");
  });

  it.each([[false, false], [false, true], [true, false]])(
    "uses host path entry for desktop=%s, shared device=%s",
    async (desktop, sharedDevice) => {
      runtime.mockReturnValue(desktop);
      vi.mocked(hostSharesDevice).mockReturnValue(sharedDevice);
      vi.mocked(requestHostFolder).mockResolvedValue("/host/project");
      await expect(pickProjectFolder()).resolves.toBe("/host/project");
      expect(dialog).not.toHaveBeenCalled();
      expect(requestHostFolder).toHaveBeenCalledOnce();
    },
  );
});
