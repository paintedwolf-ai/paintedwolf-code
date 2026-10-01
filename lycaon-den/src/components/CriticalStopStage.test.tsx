import { fireEvent, findByTestId, render, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { PreflightReport } from "../api/types.ts";
import { LycaonApiError } from "../api/http.ts";
import {
  mockAttachmentCapabilities,
  mockPreflightReport,
} from "../api/mocks/fixtures.ts";
import { CLIENT_NOTICES } from "../notices/client-notices.generated.ts";
import { createNoticeStore } from "../notices/notice-store.ts";
import { APP_SCOPE } from "../notices/notice-scope.ts";
import { createAppStore } from "../store/app-state.ts";
import { createCriticalStop } from "./CriticalStopStage.tsx";
import { setPreflightReport } from "../platform/persistence/preflight-report.ts";
import { resetPreflightStore } from "../platform/persistence/preflight-store.ts";
import {
  noteHealthResponse,
  resetStoreRevisionTracking,
} from "../platform/connection/health.ts";
import type { HealthResponse } from "../platform/connection/backend.ts";


const checkNativeUpdate = vi.fn();
const installNativeUpdate = vi.fn();
const nativeUpdateState = {
  revision: 0, phase: "idle", current_version: "1.0.0", channel: "stable",
  install_source: "direct_download", checks_enabled: false,
  rollout_eligibility: "not_applicable", downloaded_bytes: 0, total_bytes: null,
};
vi.mock("../settings/system/update-service.ts", () => ({
  nativeUpdateService: {
    getState: async () => nativeUpdateState,
    subscribe: async () => () => {},
    check: () => checkNativeUpdate(),
    install: (version: string) => installNativeUpdate(version),
  },
}));

const connectAppBackend = vi.fn();
const restartAppBackend = vi.fn();
const restoreRecoverySnapshot = vi.fn();
const resetStore = vi.fn();
const runRestoreBackup = vi.fn();
const confirmDestructive = vi.fn();
const saveDiagnosticsBundle = vi.fn();
const saveStartupDiagnosticsBundle = vi.fn();
const provideCredentialVaultPassword = vi.fn();
const resetCredentialVault = vi.fn();
let startFailure: string | null = null;
let recoveryClient: {
  restoreRecoverySnapshot: typeof restoreRecoverySnapshot;
  resetStore: typeof resetStore;
} | null = null;

vi.mock("../platform/connection/app-connection.ts", () => ({
  connectAppBackend: (...args: unknown[]) => connectAppBackend(...args),
  getLycaonClient: () => recoveryClient,
  restartAppBackend: () => restartAppBackend(),
}));

vi.mock("../settings/storage/data-backup-actions.ts", () => ({
  runRestoreBackup: (...args: unknown[]) => runRestoreBackup(...args),
}));

vi.mock("../platform/interaction/confirm-dialog.ts", () => ({
  confirmDestructive: (...args: unknown[]) => confirmDestructive(...args),
}));

vi.mock("../platform/connection/backend.ts", () => ({
  getBackendConnection: () => ({ baseUrl: "http://127.0.0.1:8787", apiToken: "t" }),
  // Startup causes are covered by the model tests.
  lastSidecarStartFailure: () => startFailure,
  lastSidecarStartDetail: () => null,
  provideCredentialVaultPassword: (...args: unknown[]) =>
    provideCredentialVaultPassword(...args),
  resetCredentialVault: () => resetCredentialVault(),
}));

vi.mock("../settings/system/diagnostics-export.ts", () => ({
  saveDiagnosticsBundle: (...args: unknown[]) => saveDiagnosticsBundle(...args),
  saveStartupDiagnosticsBundle: (...args: unknown[]) =>
    saveStartupDiagnosticsBundle(...args),
}));

const BLOCKED_REPORT: PreflightReport = {
  overall: "blocked",
  attachment_capabilities: mockAttachmentCapabilities,
  probes: [
    {
      id: "os_version",
      status: "blocked",
      tier: "catastrophic",
      resolution: "below_floor",
      code: "OS_BELOW_FLOOR",
      title: "Unsupported macOS",
      message: "Needs macOS 14 or newer.",
      suggested_action: "Update macOS, then reopen the app.",
    },
  ],
};

describe("createCriticalStop", () => {
  beforeEach(() => {
    checkNativeUpdate.mockReset();
    installNativeUpdate.mockReset();
    checkNativeUpdate.mockResolvedValue({ ...nativeUpdateState, phase: "available", available_version: "1.1.0" });
    installNativeUpdate.mockResolvedValue({ ...nativeUpdateState, phase: "restart_required", available_version: "1.1.0" });

    connectAppBackend.mockReset();
    restartAppBackend.mockReset();
    restoreRecoverySnapshot.mockReset();
    resetStore.mockReset();
    runRestoreBackup.mockReset();
    confirmDestructive.mockReset();
    confirmDestructive.mockResolvedValue(true);
    recoveryClient = null;
    saveDiagnosticsBundle.mockReset();
    saveStartupDiagnosticsBundle.mockReset();
    provideCredentialVaultPassword.mockReset();
    resetCredentialVault.mockReset();
    resetCredentialVault.mockResolvedValue(undefined);
    startFailure = null;
    resetStoreRevisionTracking();
    resetPreflightStore();
    setPreflightReport(mockPreflightReport("ok", []));
    saveDiagnosticsBundle.mockResolvedValue({ kind: "saved", path: "/tmp/report.zip" });
    saveStartupDiagnosticsBundle.mockResolvedValue({
      kind: "saved",
      path: "/tmp/startup-report.zip",
    });
  });

  function mount() {
    const appStore = createAppStore();
    let handle!: ReturnType<typeof createCriticalStop>;
    const rendered = render(() => {
      handle = createCriticalStop(appStore);
      return <>{handle.view()}</>;
    });
    return { appStore, rendered, handle: () => handle };
  }

  const recoveryHealth: HealthResponse = {
    status: "recovery",
    version: "1.0.0",
    store_revision: 1,
    schema_version: 1,
    store_schema_version: 5,
    recovery_reason: "schema_mismatch",
    recovery_detail: "schema_version 5 does not match 1",
    recovery_snapshot_available: true,
    recovery_snapshot_at: "2026-08-01T10:14:22Z",
  };

  const readyHealth: HealthResponse = {
    status: "ok",
    version: "1.0.0",
    store_revision: 2,
    schema_version: 1,
    recovery_snapshot_available: false,
  };

  it.each(["schema_mismatch", "integrity_failed"] as const)("can update in %s recovery without a working data client", async (reason) => {
    noteHealthResponse({ ...recoveryHealth, recovery_reason: reason });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");
    const root = rendered.baseElement as HTMLElement;
    const check = await findByTestId(root, "updates-check-now");
    await waitFor(() => expect((check as HTMLButtonElement).disabled).toBe(false));
    check.click();
    const install = await findByTestId(root, "updates-install");
    install.click();
    await waitFor(() => expect(installNativeUpdate).toHaveBeenCalledWith("1.1.0"));
    expect(resetStore).not.toHaveBeenCalled();
    expect(restoreRecoverySnapshot).not.toHaveBeenCalled();
  });

  it("stops the stage when the backend is unreachable", async () => {
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");

    const stop = await findByTestId(
      rendered.baseElement as HTMLElement,
      "critical-stop",
    );
    expect(stop.getAttribute("data-code")).toBe("offline");
  });

  it("keeps an established workspace available offline and still blocks store recovery", () => {
    const { appStore, handle } = mount();
    noteHealthResponse(readyHealth);
    appStore.actions.setSidecarStatus("connected");
    expect(handle().spec()).toBeUndefined();
    appStore.actions.setSidecarStatus("disconnected");
    expect(handle().spec()).toBeUndefined();
    noteHealthResponse(recoveryHealth);
    expect(handle().spec()?.code).toBe("store_incompatible");
  });

  it("renders nothing while the backend is reachable", () => {
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");
    expect(
      rendered.baseElement.querySelector('[data-testid="critical-stop"]'),
    ).toBeNull();
  });

  it("unlocks the credential vault with an explicit app password", async () => {
    startFailure = "credential_vault_locked";
    connectAppBackend.mockResolvedValue({});
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");
    const root = rendered.baseElement as HTMLElement;

    const password = await findByTestId(root, "credential-vault-password");
    fireEvent.input(password, { target: { value: "correct horse battery staple" } });
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(provideCredentialVaultPassword).toHaveBeenCalledWith(
        "correct horse battery staple",
      );
      expect(connectAppBackend).toHaveBeenCalledWith(appStore, {
        reportFailure: false,
      });
    });
  });

  it("requires matching passwords before creating a credential vault", async () => {
    startFailure = "credential_vault_uninitialized";
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");
    const root = rendered.baseElement as HTMLElement;

    fireEvent.input(await findByTestId(root, "credential-vault-password"), {
      target: { value: "correct horse battery staple" },
    });
    fireEvent.input(
      await findByTestId(root, "credential-vault-password-confirmation"),
      { target: { value: "different horse battery staple" } },
    );
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__message")?.textContent)
        .toContain("The passwords do not match.");
    });
    expect(provideCredentialVaultPassword).not.toHaveBeenCalled();
  });

  it("resets a forgotten credential vault only after confirmation", async () => {
    startFailure = "credential_vault_locked";
    connectAppBackend.mockRejectedValue(new Error("password required"));
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");
    const root = rendered.baseElement as HTMLElement;

    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => expect(resetCredentialVault).toHaveBeenCalledOnce());
    expect(confirmDestructive).toHaveBeenCalledWith(
      expect.objectContaining({ okLabel: "Reset vault" }),
    );
  });

  it("reports a failed reconnect attempt on the stop", async () => {
    const escalation = CLIENT_NOTICES.offline.suggestedAction;
    connectAppBackend.mockRejectedValue(new TypeError("Load failed"));
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");

    const root = rendered.baseElement as HTMLElement;
    const stop = await findByTestId(root, "critical-stop");
    expect(stop.textContent).not.toContain(escalation);

    root.querySelector<HTMLElement>('[data-testid="critical-stop-primary"]')?.click();

    await waitFor(() => {
      const message = root.querySelector(".den-critical-stop__message");
      expect(message?.textContent).toContain(escalation);
    });
    expect(root.querySelector('[data-testid="critical-stop-detail"]')).toBeNull();
    expect(connectAppBackend).toHaveBeenCalled();
    expect(
      root.querySelector('[data-testid="critical-stop"]')?.getAttribute("data-code"),
    ).toBe("offline");
  });

  it("leaves the notice dock empty when a reconnect attempt fails", async () => {
    connectAppBackend.mockRejectedValue(new TypeError("Load failed"));
    const notices = createNoticeStore();
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");

    const root = rendered.baseElement as HTMLElement;
    await findByTestId(root, "critical-stop");
    root.querySelector<HTMLElement>('[data-testid="critical-stop-primary"]')?.click();

    await waitFor(() => expect(connectAppBackend).toHaveBeenCalled());
    notices.reporterFor(APP_SCOPE).reportError(new TypeError("Load failed"));
    expect([...notices.index().values()].flat()).toHaveLength(0);
  });

  it("prefers the host remedy over the retry note", async () => {
    setPreflightReport(BLOCKED_REPORT);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    await waitFor(async () => {
      const detail = root.querySelector('[data-testid="critical-stop-detail"]');
      expect(detail?.textContent).toBe("Update macOS, then reopen the app.");
    });
    expect(
      root.querySelector('[data-testid="critical-stop"]')?.getAttribute("data-code"),
    ).toBe("OS_BELOW_FLOOR");
  });

  it("reports a failed readiness recheck on the stop", async () => {
    setPreflightReport(BLOCKED_REPORT);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__message")?.textContent).toContain(
        CLIENT_NOTICES.readiness_recheck_failed.message,
      );
    });
  });

  it("stages the recovery snapshot, restarts, and clears the stop", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    restoreRecoverySnapshot.mockResolvedValue({
      restart_required: true,
      recovery_copy_path: "/tmp/recovery-copy",
    });
    restartAppBackend.mockImplementation(async () => {
      noteHealthResponse(readyHealth);
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    const action = await findByTestId(root, "critical-stop-primary");
    expect(action.textContent).toContain("Restore snapshot");
    action.click();

    await waitFor(() => {
      expect(restoreRecoverySnapshot).toHaveBeenCalledOnce();
      expect(restartAppBackend).toHaveBeenCalledOnce();
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("keeps a staged restore recoverable when automatic restart fails", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    restoreRecoverySnapshot.mockResolvedValue({
      restart_required: true,
      recovery_copy_path: "/tmp/recovery-copy",
    });
    restartAppBackend.mockRejectedValueOnce(new Error("restart failed"));
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__title")?.textContent).toBe(
        "Finish restoring",
      );
      expect(
        root.querySelector('[data-testid="critical-stop-primary"]')?.textContent,
      ).toContain("Finish restore");
      expect(root.querySelector(".den-critical-stop__message")?.textContent).toContain(
        CLIENT_NOTICES.store_incompatible_restart.suggestedAction,
      );
    });

    restartAppBackend.mockImplementationOnce(async () => {
      noteHealthResponse(readyHealth);
    });
    root.querySelector<HTMLElement>('[data-testid="critical-stop-primary"]')?.click();

    await waitFor(() => {
      expect(restartAppBackend).toHaveBeenCalledTimes(2);
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("offers a backup when the recovery snapshot is incompatible", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    restoreRecoverySnapshot.mockRejectedValue(
      new LycaonApiError("different schema", 409, "backup_incompatible"),
    );
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__title")?.textContent).toBe(
        CLIENT_NOTICES.store_incompatible_snapshot_incompatible.title,
      );
      expect(
        root.querySelector('[data-testid="critical-stop-primary"]')?.textContent,
      ).toContain("Restore from backup…");
    });
  });

  it("restores a selected backup when no recovery snapshot is available", async () => {
    noteHealthResponse({
      ...recoveryHealth,
      recovery_snapshot_available: false,
      recovery_snapshot_at: undefined,
    });
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    runRestoreBackup.mockResolvedValue({
      kind: "staged",
      message: "Backup staged.",
      result: {
        restart_required: true,
        recovery_copy_path: "/tmp/recovery-copy",
      },
    });
    restartAppBackend.mockImplementation(async () => {
      noteHealthResponse(readyHealth);
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    const action = await findByTestId(root, "critical-stop-primary");
    expect(action.textContent).toContain("Restore from backup…");
    action.click();

    await waitFor(() => {
      expect(runRestoreBackup).toHaveBeenCalledWith(recoveryClient, expect.objectContaining({ signal: expect.any(AbortSignal), onProgress: expect.any(Function) }));
      expect(restartAppBackend).toHaveBeenCalledOnce();
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("offers a confirmed fresh start from every incompatible-store stop", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    resetStore.mockResolvedValue({
      restart_required: true,
      recovery_copy_path: "/tmp/fresh-start-recovery",
    });
    restartAppBackend.mockImplementation(async () => {
      noteHealthResponse(readyHealth);
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    const action = await findByTestId(root, "critical-stop-secondary");
    expect(action.textContent).toContain("Start fresh…");
    action.click();

    await waitFor(() => {
      expect(confirmDestructive).toHaveBeenCalledWith(
        expect.objectContaining({
          title: "Start fresh?",
          okLabel: "Start fresh",
        }),
      );
      expect(resetStore).toHaveBeenCalledOnce();
      expect(restartAppBackend).toHaveBeenCalledOnce();
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("does not reset the store when fresh start is cancelled", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    confirmDestructive.mockResolvedValue(false);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => expect(confirmDestructive).toHaveBeenCalledOnce());
    expect(resetStore).not.toHaveBeenCalled();
    expect(root.querySelector('[data-testid="critical-stop"]')).not.toBeNull();
  });

  it("keeps a staged fresh start recoverable when automatic restart fails", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    resetStore.mockResolvedValue({
      restart_required: true,
      recovery_copy_path: "/tmp/fresh-start-recovery",
    });
    restartAppBackend.mockRejectedValueOnce(new Error("restart failed"));
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__title")?.textContent).toBe(
        "Finish starting fresh",
      );
      expect(
        root.querySelector('[data-testid="critical-stop-primary"]')?.textContent,
      ).toContain("Finish starting fresh");
      expect(root.querySelector('[data-testid="critical-stop-secondary"]')).toBeNull();
    });
  });

  it("treats backup_restore_pending during start fresh as already staged and finishes restart", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    resetStore.mockRejectedValue(
      new LycaonApiError("restore is staged", 409, "backup_restore_pending"),
    );
    restartAppBackend.mockImplementation(async () => {
      noteHealthResponse(readyHealth);
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => {
      expect(resetStore).toHaveBeenCalledOnce();
      expect(restartAppBackend).toHaveBeenCalledOnce();
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("keeps fresh start visible when restart fails after pending restore", async () => {
    noteHealthResponse(recoveryHealth);
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    resetStore.mockRejectedValue(
      new LycaonApiError("restore is staged", 409, "backup_restore_pending"),
    );
    restartAppBackend.mockRejectedValueOnce(new Error("restart failed"));
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => {
      expect(root.querySelector(".den-critical-stop__title")?.textContent).toBe(
        "Finish starting fresh",
      );
      expect(
        root.querySelector('[data-testid="critical-stop-primary"]')?.textContent,
      ).toContain("Finish starting fresh");
      expect(root.querySelector('[data-testid="critical-stop-secondary"]')).toBeNull();
    });
  });

  it("proceeds to restart when snapshot restore encounters pending restore", async () => {
    noteHealthResponse({
      ...recoveryHealth,
      recovery_snapshot_available: true,
      recovery_snapshot_at: "2026-08-01T10:14:22Z",
    });
    recoveryClient = { restoreRecoverySnapshot, resetStore };
    restoreRecoverySnapshot.mockRejectedValue(
      new LycaonApiError("restore is staged", 409, "backup_restore_pending"),
    );
    restartAppBackend.mockImplementation(async () => {
      noteHealthResponse(readyHealth);
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-primary")).click();

    await waitFor(() => {
      expect(restoreRecoverySnapshot).toHaveBeenCalledOnce();
      expect(restartAppBackend).toHaveBeenCalledOnce();
      expect(root.querySelector('[data-testid="critical-stop"]')).toBeNull();
    });
  });

  it("saves the diagnostics bundle from a host-declared stop", async () => {
    setPreflightReport(BLOCKED_REPORT);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    const save = (await findByTestId(
      root,
      "critical-stop-secondary",
    )) as HTMLButtonElement;
    expect(save.textContent).toContain("Save report bundle");
    save.click();

    await waitFor(() => {
      expect(saveDiagnosticsBundle).toHaveBeenCalled();
      expect(
        root.querySelector('[data-testid="critical-stop-status"]')?.textContent,
      ).toContain("/tmp/report.zip");
    });
  });

  it("reports a failed save without hiding the host remedy", async () => {
    saveDiagnosticsBundle.mockResolvedValue({ kind: "failed", detail: "disk full" });
    setPreflightReport(BLOCKED_REPORT);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => {
      expect(
        root.querySelector('[data-testid="critical-stop-status"]')?.textContent,
      ).toContain("disk full");
    });
    expect(
      root.querySelector('[data-testid="critical-stop-detail"]')?.textContent,
    ).toBe("Update macOS, then reopen the app.");
  });

  it("says nothing when the save dialog is cancelled", async () => {
    saveDiagnosticsBundle.mockResolvedValue({ kind: "cancelled" });
    setPreflightReport(BLOCKED_REPORT);
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("connected");

    const root = rendered.baseElement as HTMLElement;
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => expect(saveDiagnosticsBundle).toHaveBeenCalled());
    expect(root.querySelector('[data-testid="critical-stop-status"]')).toBeNull();
  });

  it("saves a startup bundle on the offline stop", async () => {
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");

    const root = rendered.baseElement as HTMLElement;
    await findByTestId(root, "critical-stop");
    const save = await findByTestId(root, "critical-stop-secondary");
    save.click();

    await waitFor(() => {
      expect(saveStartupDiagnosticsBundle).toHaveBeenCalled();
      expect(
        root.querySelector('[data-testid="critical-stop-status"]')?.textContent,
      ).toContain("/tmp/startup-report.zip");
    });
    expect(saveDiagnosticsBundle).not.toHaveBeenCalled();
  });

  it("keeps the offline remedy visible when the startup collector fails", async () => {
    saveStartupDiagnosticsBundle.mockResolvedValue({
      kind: "failed",
      detail: "startup diagnostics command failed",
    });
    const { appStore, rendered } = mount();
    appStore.actions.setSidecarStatus("disconnected");

    const root = rendered.baseElement as HTMLElement;
    await findByTestId(root, "critical-stop");
    (await findByTestId(root, "critical-stop-secondary")).click();

    await waitFor(() => {
      expect(
        root.querySelector('[data-testid="critical-stop-status"]')?.textContent,
      ).toContain("startup diagnostics command failed");
    });
    expect(root.querySelector('[data-testid="critical-stop-primary"]')).not.toBeNull();
    expect(saveDiagnosticsBundle).not.toHaveBeenCalled();
  });
});
