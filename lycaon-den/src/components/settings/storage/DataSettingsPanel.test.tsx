import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import * as backupActions from "../../../settings/storage/data-backup-actions.ts";
import * as runtime from "../../../platform/runtime.ts";
import { DataSettingsPanel } from "./DataSettingsPanel.tsx";
import { DATA_SETTINGS_COPY } from "../../../settings/storage/data-settings-copy.ts";
import { LycaonApiError } from "../../../api/http.ts";
import type { DownloadExportResult } from "../../../platform/files/save-file.ts";

vi.mock("./HistoryStorageSettingsPanel.tsx", () => ({ HistoryStorageSettingsPanel: () => null }));

const downloadExport = vi.fn<() => Promise<DownloadExportResult>>();
const pickBackupArchive = vi.fn();
const restartAppBackend = vi.fn(async () => undefined);

vi.mock("../../../platform/files/save-file.ts", () => ({
  downloadExport: (...args: unknown[]) => downloadExport(...(args as [])),
}));

vi.mock("../../../platform/files/folder.ts", () => ({
  pickBackupArchive: (...args: unknown[]) =>
    pickBackupArchive(...(args as [])),
}));

vi.mock("../../../platform/connection/app-connection.ts", () => ({
  restartAppBackend: () => restartAppBackend(),
}));

describe("DataSettingsPanel", () => {
  beforeEach(() => {
    downloadExport.mockReset().mockResolvedValue({ kind: "saved", path: "/tmp/backup.zip" });
    pickBackupArchive.mockReset();
    restartAppBackend.mockClear();
  });

  it("shows the secret-exclusion note and Data controls when online", () => {
    const client = {
      exportBackup: vi.fn(),
      restoreBackup: vi.fn(),
    };
    const { getByTestId } = render(() => (
      <DataSettingsPanel client={client as never} />
    ));
    expect(getByTestId("data-backup-secrets-note").textContent).toContain(
      DATA_SETTINGS_COPY.secretsNote,
    );
    expect(getByTestId("data-backup-now")).toBeTruthy();
    expect(getByTestId("data-restore")).toBeTruthy();
  });

  it("backs up without asking about credentials — archives never carry them", async () => {
    const blob = new Blob(["zip"]);
    const exportBackup = vi.fn().mockResolvedValue({
      blob,
      filename: "painted-wolf-backup-2026-07-28.zip",
    });
    const client = { exportBackup, restoreBackup: vi.fn() };

    const { getByTestId } = render(() => (
      <DataSettingsPanel client={client as never} />
    ));

    fireEvent.click(getByTestId("data-backup-now"));

    await waitFor(() => {
      expect(exportBackup).toHaveBeenCalledWith();
      expect(downloadExport).toHaveBeenCalledWith(
        blob,
        "painted-wolf-backup-2026-07-28.zip",
      );
      expect(getByTestId("data-backup-status").textContent).toBe(DATA_SETTINGS_COPY.backupSaved);
    });
  });

  it("does not report success or error when the native save is cancelled", async () => {
    const client = { exportBackup: vi.fn().mockResolvedValue({ blob: new Blob(["zip"]), filename: "backup.zip" }) };
    const { getByTestId, queryByTestId } = render(() => <DataSettingsPanel client={client as never} />);
    fireEvent.click(getByTestId("data-backup-now"));
    await waitFor(() => expect(queryByTestId("data-backup-status")).not.toBeNull());
    downloadExport.mockResolvedValue({ kind: "cancelled" });
    fireEvent.click(getByTestId("data-backup-now"));
    await waitFor(() => expect((getByTestId("data-backup-now") as HTMLButtonElement).disabled).toBe(false));
    expect(queryByTestId("data-backup-status")).toBeNull();
    expect(queryByTestId("data-action-error")).toBeNull();
    expect(downloadExport).toHaveBeenCalledTimes(2);
  });

  it("reports a save failure and leaves backup available to retry", async () => {
    downloadExport.mockRejectedValue(new Error("disk full"));
    const client = { exportBackup: vi.fn().mockResolvedValue({ blob: new Blob(["zip"]), filename: "backup.zip" }) };
    const { getByTestId, queryByTestId } = render(() => <DataSettingsPanel client={client as never} />);
    fireEvent.click(getByTestId("data-backup-now"));
    await waitFor(() => expect(getByTestId("data-action-error").textContent).toBe("disk full"));
    expect(queryByTestId("data-backup-status")).toBeNull();
    expect((getByTestId("data-backup-now") as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows incomplete-backup guidance without offering a download", async () => {
    const client = {
      exportBackup: vi.fn().mockRejectedValue(new LycaonApiError(
        "Retained data is missing.", 409, "backup_incomplete",
        { suggestedAction: "Let active changes finish, then try again." },
      )),
    };
    const { getByTestId, queryByTestId } = render(() => <DataSettingsPanel client={client as never} />);
    fireEvent.click(getByTestId("data-backup-now"));
    await waitFor(() => expect(getByTestId("data-action-error").textContent).toBe(
      "Retained data is missing. Let active changes finish, then try again.",
    ));
    expect(downloadExport).not.toHaveBeenCalled();
    expect(queryByTestId("data-backup-status")).toBeNull();
    expect((getByTestId("data-backup-now") as HTMLButtonElement).disabled).toBe(false);
  });

  it("keeps restart guidance visible for an externally managed sidecar", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    pickBackupArchive.mockResolvedValue(new Uint8Array([1, 2, 3]));
    const restoreBackup = vi.fn().mockResolvedValue({
      restart_required: true,
      recovery_copy_path: "/tmp/.restore-recovery",
    });
    const client = { exportBackup: vi.fn(), restoreBackup };

    const { getByTestId } = render(() => (
      <DataSettingsPanel client={client as never} />
    ));
    fireEvent.click(getByTestId("data-restore"));

    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalled();
      expect(restoreBackup).toHaveBeenCalled();
      expect(restartAppBackend).not.toHaveBeenCalled();
      expect(getByTestId("data-restore-staged").textContent).toContain(
        "Restart Painted Wolf Code",
      );
    });
    confirmSpy.mockRestore();
  });

  it("restarts the managed desktop sidecar after staging restore", async () => {
    const staged = {
      kind: "staged" as const,
      message: "Restart Painted Wolf Code",
      result: { restart_required: true, recovery_copy_path: "/tmp/.restore-recovery" },
    };
    const restore = vi.spyOn(backupActions, "runRestoreBackup").mockResolvedValue(staged);
    const native = vi.spyOn(runtime, "isTauriRuntime").mockReturnValue(true);
    try {
      const { getByTestId } = render(() => <DataSettingsPanel client={{} as never} />);
      fireEvent.click(getByTestId("data-restore"));
      await waitFor(() => {
        expect(restartAppBackend).toHaveBeenCalledTimes(1);
        expect(getByTestId("data-restore-staged").textContent).toBe(DATA_SETTINGS_COPY.restoreRestarted);
      });
    } finally {
      restore.mockRestore();
      native.mockRestore();
    }
  });

  it("maps backup_invalid and backup_incompatible errors", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    pickBackupArchive.mockResolvedValue(new Uint8Array([9]));
    const restoreBackup = vi
      .fn()
      .mockRejectedValueOnce(
        new LycaonApiError("bad", 400, "backup_invalid"),
      )
      .mockRejectedValueOnce(
        new LycaonApiError("incompatible", 409, "backup_incompatible"),
      );
    const client = { exportBackup: vi.fn(), restoreBackup };

    const { getByTestId } = render(() => (
      <DataSettingsPanel client={client as never} />
    ));

    fireEvent.click(getByTestId("data-restore"));
    await waitFor(() => {
      expect(getByTestId("data-action-error").textContent).toBe(
        DATA_SETTINGS_COPY.invalidBackup,
      );
    });

    fireEvent.click(getByTestId("data-restore"));
    await waitFor(() => {
      expect(getByTestId("data-action-error").textContent).toBe(
        DATA_SETTINGS_COPY.incompatibleBackup,
      );
    });
    confirmSpy.mockRestore();
  });

  it("does not restore when confirm is cancelled", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    pickBackupArchive.mockResolvedValue(new Uint8Array([1]));
    const restoreBackup = vi.fn();
    const client = { exportBackup: vi.fn(), restoreBackup };

    const { getByTestId } = render(() => (
      <DataSettingsPanel client={client as never} />
    ));
    fireEvent.click(getByTestId("data-restore"));

    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalled();
    });
    expect(restoreBackup).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });
});
