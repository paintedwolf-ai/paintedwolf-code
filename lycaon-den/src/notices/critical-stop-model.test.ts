import { describe, expect, it } from "vitest";
import type { PreflightReport } from "../api/types.ts";
import { mockPreflightReport } from "../api/mocks/fixtures.ts";
import {
  ENGINE_LOCKED_STOP_CODE,
  ENGINE_STOPPED_STOP_CODE,
  HOST_INCOMPATIBLE_STOP_CODE,
  ENGINE_NOT_STARTED_STOP_CODE,
  OFFLINE_STOP_CODE,
  STARTUP_CANCELLED_STOP_CODE,
  STORE_INCOMPATIBLE_STOP_CODE,
  criticalStopRecoveryLabel,
  resolveCriticalStop,
  storeIncompatibleCriticalStop,
} from "./critical-stop-model.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { testHostInfo } from "../platform/connection/host-identity-test.ts";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

function report(
  overall: PreflightReport["overall"],
  probes: PreflightReport["probes"],
): PreflightReport {
	return mockPreflightReport(overall, probes);
}

describe("resolveCriticalStop", () => {
  it("does not stop a reachable stage with nothing wrong", () => {
    expect(resolveCriticalStop({ sidecarStatus: "connected" })).toBeUndefined();
  });

  it("does not stop a host serving the same major contract", () => {
    expect(resolveCriticalStop({
      sidecarStatus: "connected",
      host: testHostInfo({ contract_version: "1.9.4" }),
    })).toBeUndefined();
  });

  it("blocks a host serving a different major contract", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      host: testHostInfo({ contract_version: "2.0.0" }),
    });
    expect(stop?.code).toBe(HOST_INCOMPATIBLE_STOP_CODE);
    expect(stop?.recovery).toBe("reconnect");
    expect(stop?.message).toBe(CLIENT_NOTICES.host_incompatible.message);
    expect(stop?.retryFailed).toBe(CLIENT_NOTICES.host_incompatible.suggestedAction);
    expect(stop?.diagnostic).toContain("2.0.0");
  });

  it("presents store recovery before an incompatible contract", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      health: {
        status: "recovery",
        version: "0.0.0",
        store_revision: 1,
        schema_version: 1,
        recovery_reason: "schema_mismatch",
        recovery_snapshot_available: false,
      },
      host: testHostInfo({ contract_version: "2.0.0" }),
    });
    expect(stop?.code).toBe(STORE_INCOMPATIBLE_STOP_CODE);
  });

  it("stops on an unreachable backend", () => {
    const stop = resolveCriticalStop({ sidecarStatus: "disconnected" });
    expect(stop?.code).toBe(OFFLINE_STOP_CODE);
    expect(stop?.recovery).toBe("reconnect");
  });

  it("stops a window that already admitted its workspace once the shell gives up on the engine", () => {
    const exit = { signal: 9, description: "was killed by signal 9 (SIGKILL)" };
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      offlineAvailable: true,
      engine: { state: "stopped", exit },
    });
    expect(stop).toMatchObject({
      code: ENGINE_STOPPED_STOP_CODE,
      title: CLIENT_NOTICES.engine_stopped.title,
      recovery: "reconnect",
      diagnostic: "The engine was killed by signal 9 (SIGKILL).",
    });
    expect(resolveCriticalStop({
      sidecarStatus: "disconnected",
      engine: { state: "stopped", exit, failure: "another engine is already serving this store" },
    })?.diagnostic).toBe("The engine was killed by signal 9 (SIGKILL). Restarting it failed: another engine is already serving this store");
  });

  it("holds a window in place while the shell restarts its engine", () => {
    expect(resolveCriticalStop({
      sidecarStatus: "disconnected",
      offlineAvailable: true,
      engine: { state: "restarting", exit: { code: 2, description: "exited with status 2" }, attempt: 1 },
    })).toBeUndefined();
  });

  it("names a held store lock instead of telling the user to retry", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      startFailure: "store_locked",
    });
    expect(stop?.code).toBe(ENGINE_LOCKED_STOP_CODE);
    expect(stop?.message).toBe(CLIENT_NOTICES.engine_already_running.message);
    expect(stop?.message).not.toBe(CLIENT_NOTICES.offline.message);
  });

  it("names a missing development engine instead of offering a reopen", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      startFailure: "engine_not_started",
    });
    expect(stop?.code).toBe(ENGINE_NOT_STARTED_STOP_CODE);
    expect(stop?.message).toBe(CLIENT_NOTICES.engine_not_started.message);
    expect(`${stop?.message} ${stop?.retryFailed}`.toLowerCase()).not.toContain("reopen");
  });

  it("presents an explicitly cancelled launch as stopped rather than failed", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      startFailure: "cancelled",
      startDetail: "Engine startup was stopped.",
    });
    expect(stop?.code).toBe(STARTUP_CANCELLED_STOP_CODE);
    expect(stop?.message).toBe(CLIENT_NOTICES.startup_cancelled.message);
    expect(stop?.diagnostic).toBeUndefined();
  });

  it.each([
    ["credential_vault_locked", "unlock_credential_vault"],
    ["credential_vault_unlock_failed", "unlock_credential_vault"],
    ["credential_vault_uninitialized", "create_credential_vault"],
    ["credential_vault_corrupt", "reset_credential_vault"],
  ] as const)("maps %s to its vault recovery", (startFailure, recovery) => {
    const stop = resolveCriticalStop({ sidecarStatus: "disconnected", startFailure });
    expect(stop?.code).toBe(startFailure);
    expect(stop?.recovery).toBe(recovery);
    expect(stop?.diagnostic).toBeUndefined();
  });

  it.each([
    "store_locked",
    "engine_not_started",
    "cancelled",
    "credential_vault_locked",
    "credential_vault_uninitialized",
    "credential_vault_unlock_failed",
    "credential_vault_corrupt",
    undefined,
  ] as const)(
    "holds the escalation for %s until a retry fails",
    (startFailure) => {
      const stop = resolveCriticalStop({ sidecarStatus: "disconnected", startFailure });
      expect(stop?.retryFailed).toBeTruthy();
      expect(stop?.detail).toBeUndefined();
    },
  );

  it("does not spend the escalation inside the offline message", () => {
    const stop = resolveCriticalStop({ sidecarStatus: "disconnected" });
    expect(stop?.message.toLowerCase()).not.toContain("reopen");
    expect(stop?.retryFailed?.toLowerCase()).toContain("reopening");
  });

  it("carries the host's failure evidence on the generic offline stop", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      startDetail: "engine exited before publishing a port\nlog tail: panic at boot",
    });
    expect(stop?.diagnostic).toContain("panic at boot");
  });

  it("keeps the held-lock stop free of the start diagnostic", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      startFailure: "store_locked",
      startDetail: "engine exited before publishing a port",
    });
    expect(stop?.diagnostic).toBeUndefined();
  });

  it("falls back to the generic stop when the host named no reason", () => {
    for (const startFailure of [undefined, null] as const) {
      const stop = resolveCriticalStop({ sidecarStatus: "disconnected", startFailure });
      expect(stop?.code).toBe(OFFLINE_STOP_CODE);
    }
  });

  it("ignores a stale start reason once the backend is reachable", () => {
    expect(
      resolveCriticalStop({ sidecarStatus: "connected", startFailure: "store_locked" }),
    ).toBeUndefined();
  });

  it.each(["connecting", "reconnecting", "connected"] as const)(
    "treats %s as reachable",
    (status) => {
      expect(resolveCriticalStop({ sidecarStatus: status })).toBeUndefined();
    },
  );

  it("stops on a catastrophic probe, using wire copy", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      preflight: report("blocked", [
        { id: "config_dir", status: "ok" },
        {
          id: "os_version",
          status: "blocked",
          tier: "catastrophic",
          resolution: "below_floor",
          code: "OS_BELOW_FLOOR",
          title: "macOS is too old",
          message: "This app needs macOS 14 or newer.",
          suggested_action: "Update macOS, then reopen the app.",
        },
      ]),
    });
    expect(stop).toEqual({
      code: "OS_BELOW_FLOOR",
      title: "macOS is too old",
      message: "This app needs macOS 14 or newer.",
      detail: "Update macOS, then reopen the app.",
      retryFailed: CLIENT_NOTICES.readiness_recheck_failed.message,
      recovery: "recheck",
    });
  });

  it("follows the host tier, not the probe status", () => {
    const notAStop = resolveCriticalStop({
      sidecarStatus: "connected",
      preflight: report("blocked", [
        {
          id: "os_version",
          status: "blocked",
          tier: "non_catastrophic",
          resolution: "unreadable",
          code: "OS_BELOW_FLOOR",
        },
      ]),
    });
    expect(notAStop).toBeUndefined();

    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      preflight: report("degraded", [
        {
          id: "config_dir",
          status: "degraded",
          tier: "catastrophic",
          code: "CONFIG_DIR_UNWRITABLE",
          title: "Cannot write settings",
          message: "m",
        },
      ]),
    });
    expect(stop?.code).toBe("CONFIG_DIR_UNWRITABLE");
  });

  it("does not stop on a degraded report", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      preflight: report("degraded", [
        {
          id: "browser_engine",
          status: "degraded",
          code: "BROWSER_ENGINE_UNAVAILABLE",
        },
      ]),
    });
    expect(stop).toBeUndefined();
  });

  it("prefers offline over a catastrophic probe", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      preflight: report("blocked", [
        { id: "os_version", status: "blocked", tier: "catastrophic", code: "OS_BELOW_FLOOR" },
      ]),
    });
    expect(stop?.code).toBe(OFFLINE_STOP_CODE);
  });

  it("falls back to the probe id when copy has not shipped", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      preflight: report("blocked", [
        { id: "config_dir", status: "blocked", tier: "catastrophic" },
      ]),
    });
    expect(stop?.code).toBe("config_dir");
    expect(stop?.title).toBe("config_dir");
  });
});

describe("criticalStopRecoveryLabel", () => {
  it("labels each recovery", () => {
    expect(criticalStopRecoveryLabel("reconnect")).toBe("Try again");
    expect(criticalStopRecoveryLabel("recheck")).toBe("Check again");
    expect(criticalStopRecoveryLabel("restore_snapshot")).toBe("Restore snapshot");
    expect(criticalStopRecoveryLabel("restore_backup")).toBe("Restore from backup…");
    expect(criticalStopRecoveryLabel("finish_restore")).toBe("Finish restore");
    expect(criticalStopRecoveryLabel("finish_fresh_start")).toBe(
      "Finish starting fresh",
    );
  });
});

describe("store incompatible critical stop", () => {
  const recoveryHealth = {
    status: "recovery" as const,
    version: "0.0.0",
    store_revision: 1,
    schema_version: 1,
    store_schema_version: 9,
    recovery_reason: "integrity_failed" as const,
    recovery_detail: "detail",
    recovery_snapshot_available: true,
    recovery_snapshot_at: "2026-08-01T10:14:22Z",
  };

  it("selects the store-incompatible stop from health.status", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "connected",
      health: recoveryHealth,
    });
    expect(stop?.code).toBe(STORE_INCOMPATIBLE_STOP_CODE);
    expect(stop?.recovery).toBe("restore_snapshot");
    expect(criticalStopRecoveryLabel(stop!.recovery)).toBe("Restore snapshot");
    expect(stop?.diagnostic).toBe("detail");
    expect(stop?.message).toContain(
      new Date(recoveryHealth.recovery_snapshot_at).toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      }),
    );
  });

  it("prefers reconnect when recovery health is stale", () => {
    const stop = resolveCriticalStop({
      sidecarStatus: "disconnected",
      health: recoveryHealth,
    });
    expect(stop?.code).toBe(OFFLINE_STOP_CODE);
  });

  it("offers a backup and omits the time when no integrity snapshot is available", () => {
    const stop = storeIncompatibleCriticalStop({
      ...recoveryHealth,
      recovery_snapshot_available: false,
      recovery_snapshot_at: undefined,
    });
    expect(stop.recovery).toBe("restore_backup");
    expect(stop.message).toBe(CLIENT_NOTICES.store_incompatible_integrity_no_snapshot.message);
    expect(stop.message).not.toMatch(/\d{4}/);
  });

  it("uses schema-mismatch copy and keeps the host detail as diagnostic evidence", () => {
    const stop = storeIncompatibleCriticalStop({
      ...recoveryHealth,
      store_schema_version: 5,
      recovery_reason: "schema_mismatch",
      recovery_detail: "schema_version 5 does not match 1",
    });
    expect(stop.title).toBe(CLIENT_NOTICES.store_incompatible_schema.title);
    expect(stop.message).toContain("different data format");
    expect(stop.message).not.toContain("integrity check");
    expect(stop.detail).toBeUndefined();
    expect(stop.diagnostic).toBe("schema_version 5 does not match 1");
  });

  it("offers backup restore when a schema mismatch has no snapshot", () => {
    const stop = storeIncompatibleCriticalStop({
      ...recoveryHealth,
      recovery_reason: "schema_mismatch",
      recovery_snapshot_available: false,
      recovery_snapshot_at: undefined,
    });
    expect(stop.recovery).toBe("restore_backup");
    expect(stop.message).toBe(CLIENT_NOTICES.store_incompatible_schema_no_snapshot.message);
  });

  it("keeps recovery copy out of the model source", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const src = readFileSync(join(here, "critical-stop-model.ts"), "utf8");
    for (const fragment of [
      "Your data needs attention",
      "Finish restoring",
    ]) {
      expect(src, fragment).not.toContain(fragment);
    }
  });
});
