import { beforeEach, expect, it, vi } from "vitest";
import { runBackupNow, runRestoreBackup } from "./data-backup-actions.ts";
import { stubClient } from "../../test/client-fixture.ts";

const { pick, transfer, confirm } = vi.hoisted(() => ({ pick: vi.fn(), transfer: vi.fn(), confirm: vi.fn() }));
vi.mock("../../platform/runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("../../platform/persistence/backup-transfer.ts", () => ({ pickNativeBackup: pick, transferNativeBackup: transfer }));
vi.mock("../../platform/interaction/confirm-dialog.ts", () => ({ confirmDestructive: confirm }));

beforeEach(() => {
  vi.clearAllMocks();
  pick.mockResolvedValue("grant");
  transfer.mockResolvedValue({ saved: true });
  confirm.mockResolvedValue(true);
});

it("desktop export never requests a renderer Blob", async () => {
  const client = stubClient({ exportBackup: vi.fn() });
  await expect(runBackupNow(client)).resolves.toEqual({ kind: "saved" });
  expect(client.exportBackup).not.toHaveBeenCalled();
  expect(transfer).toHaveBeenCalledWith("grant", false, undefined);
});

it("a cancelled picker performs no export", async () => {
  pick.mockResolvedValue(null);
  await expect(runBackupNow(stubClient())).resolves.toEqual({ kind: "cancelled" });
  expect(transfer).not.toHaveBeenCalled();
});

it("restore obtains confirmation before sending the selected file", async () => {
  confirm.mockResolvedValue(false);
  await expect(runRestoreBackup(stubClient())).resolves.toEqual({ kind: "cancelled" });
  expect(transfer).not.toHaveBeenCalled();
});

it("restores through the grant without reading file bytes into the renderer", async () => {
  const result = { restart_required: true, recovery_copy_path: "/data/recovery" };
  transfer.mockResolvedValue(result);
  const client = stubClient({ restoreBackup: vi.fn() });
  await expect(runRestoreBackup(client)).resolves.toMatchObject({ kind: "staged", result });
  expect(client.restoreBackup).not.toHaveBeenCalled();
  expect(transfer).toHaveBeenCalledWith("grant", true, undefined);
});
