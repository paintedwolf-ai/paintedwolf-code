import type { CatastrophicDetail, HostInfo } from "../api/types.ts";
import { API_CONTRACT_VERSION } from "../api/operations.generated.ts";
import { contractCompatible } from "../platform/connection/host-identity.ts";
import type { PreflightProbe, PreflightReport } from "../api/types.ts";
import type { HealthResponse } from "../platform/connection/backend.ts";
import type { SidecarStartFailure } from "../platform/connection/backend.ts";
import type { EngineState } from "../platform/connection/engine-supervision.ts";
import { isBackendReachable } from "../platform/connection/sidecar-status.ts";
import type { SidecarStatus } from "../store/app-state-model.ts";
import { CLIENT_NOTICES, type ClientNoticeCopy } from "./client-notices.generated.ts";

export type CriticalStopRecovery =
  | "reconnect"
  | "recheck"
  | "restore_snapshot"
  | "restore_backup"
  | "finish_restore"
  | "finish_fresh_start"
  | "unlock_credential_vault"
  | "create_credential_vault"
  | "reset_credential_vault";

export type CriticalStopSpec = {
  code: string;
  title: string;
  message: string;
  /** Host-authored remedy. */
  detail?: string;
  /** Copy shown after a failed recovery attempt. */
  retryFailed?: string;
  recovery: CriticalStopRecovery;
  /** Host stop-screen report, when the readiness read reached us. */
  catastrophicDetail?: CatastrophicDetail;
  /** Selectable host failure evidence. */
  diagnostic?: string;
};

export function criticalStopRecoveryLabel(
  recovery: CriticalStopRecovery,
): string {
  switch (recovery) {
    case "reconnect":
      return "Try again";
    case "recheck":
      return "Check again";
    case "restore_snapshot":
      return "Restore snapshot";
    case "restore_backup":
      return "Restore from backup…";
    case "finish_restore":
      return "Finish restore";
    case "finish_fresh_start":
      return "Finish starting fresh";
    case "unlock_credential_vault":
      return "Unlock vault";
    case "create_credential_vault":
      return "Create vault";
    case "reset_credential_vault":
      return "Reset vault…";
  }
}

/** Backend unreachable before host copy can load. */
export const OFFLINE_STOP_CODE = "offline";

export const ENGINE_LOCKED_STOP_CODE = "engine_already_running";

/** Attach-only development build with no engine to attach to. */
export const ENGINE_NOT_STARTED_STOP_CODE = "engine_not_started";

export const STARTUP_CANCELLED_STOP_CODE = "startup_cancelled";

const NAMED_START_FAILURES: Record<
  SidecarStartFailure,
  { code: string; copy: ClientNoticeCopy }
> = {
  store_locked: {
    code: ENGINE_LOCKED_STOP_CODE,
    copy: CLIENT_NOTICES.engine_already_running,
  },
  engine_not_started: {
    code: ENGINE_NOT_STARTED_STOP_CODE,
    copy: CLIENT_NOTICES.engine_not_started,
  },
  cancelled: {
    code: STARTUP_CANCELLED_STOP_CODE,
    copy: CLIENT_NOTICES.startup_cancelled,
  },
  credential_vault_locked: {
    code: "credential_vault_locked",
    copy: CLIENT_NOTICES.credential_vault_locked,
  },
  credential_vault_uninitialized: {
    code: "credential_vault_uninitialized",
    copy: CLIENT_NOTICES.credential_vault_uninitialized,
  },
  credential_vault_unlock_failed: {
    code: "credential_vault_unlock_failed",
    copy: CLIENT_NOTICES.credential_vault_unlock_failed,
  },
  credential_vault_corrupt: {
    code: "credential_vault_corrupt",
    copy: CLIENT_NOTICES.credential_vault_corrupt,
  },
};

function offlineCriticalStop(
  startFailure?: SidecarStartFailure | null,
  startDetail?: string | null,
): CriticalStopSpec {
  const named = startFailure ? NAMED_START_FAILURES[startFailure] : undefined;
  const copy = named?.copy ?? CLIENT_NOTICES.offline;
  const recovery: CriticalStopRecovery = startFailure === "credential_vault_uninitialized"
    ? "create_credential_vault"
    : startFailure === "credential_vault_locked" ||
        startFailure === "credential_vault_unlock_failed"
      ? "unlock_credential_vault"
      : startFailure === "credential_vault_corrupt"
        ? "reset_credential_vault"
        : "reconnect";
  return {
    code: named?.code ?? OFFLINE_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: copy.suggestedAction,
    recovery,
    // Known startup failures already have actionable catalog copy.
    diagnostic:
      startFailure === "store_locked" ||
        startFailure === "cancelled" ||
        startFailure?.startsWith("credential_vault_")
        ? undefined
        : startDetail?.trim() || undefined,
  };
}

/** The shell stopped restarting an engine that kept exiting or could not start again. */
export const ENGINE_STOPPED_STOP_CODE = "engine_stopped";

export function engineStoppedCriticalStop(
  engine: Extract<EngineState, { state: "stopped" }>,
): CriticalStopSpec {
  const copy = CLIENT_NOTICES.engine_stopped;
  const exited = `The engine ${engine.exit.description}.`;
  return {
    code: ENGINE_STOPPED_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: copy.suggestedAction,
    recovery: "reconnect",
    diagnostic: engine.failure ? `${exited} Restarting it failed: ${engine.failure}` : exited,
  };
}

/** Store this build cannot open — health.status === "recovery". */
export const STORE_INCOMPATIBLE_STOP_CODE = "store_incompatible";

/** Host serving a different major contract than this build. */
export const HOST_INCOMPATIBLE_STOP_CODE = "host_incompatible";

export type CriticalStopInput = {
  sidecarStatus: SidecarStatus;
  /** This window has already admitted a usable workspace. */
  offlineAvailable?: boolean;
  startFailure?: SidecarStartFailure | null;
  startDetail?: string | null;
  preflight?: PreflightReport;
  health?: HealthResponse | null;
  /** Host handshake, once read. */
  host?: HostInfo | null;
  /** The engine the shell supervises, when it launched one. */
  engine?: EngineState;
};

export function formatRecoverySnapshotAt(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

function fillTime(template: string, time: string | undefined): string {
  if (!time) return template.replace(/\s*from \{time\}/g, "").replace("{time}", "");
  return template.replaceAll("{time}", time);
}

function storeIncompatibleCopy(
  health: HealthResponse,
  hasSnapshot: boolean,
): ClientNoticeCopy {
  if (health.recovery_reason === "schema_mismatch") {
    return hasSnapshot
      ? CLIENT_NOTICES.store_incompatible_schema
      : CLIENT_NOTICES.store_incompatible_schema_no_snapshot;
  }
  return hasSnapshot
    ? CLIENT_NOTICES.store_incompatible_integrity
    : CLIENT_NOTICES.store_incompatible_integrity_no_snapshot;
}

export function storeIncompatibleCriticalStop(
  health: HealthResponse,
): CriticalStopSpec {
  const at = health.recovery_snapshot_at?.trim();
  const time = at ? formatRecoverySnapshotAt(at) : undefined;
  const hasSnapshot = health.recovery_snapshot_available === true && Boolean(at);
  const copy = storeIncompatibleCopy(health, hasSnapshot);

  return {
    code: STORE_INCOMPATIBLE_STOP_CODE,
    title: copy.title,
    message: fillTime(copy.message, hasSnapshot ? time : undefined),
    retryFailed: CLIENT_NOTICES.store_incompatible_restore_failed.message,
    recovery: hasSnapshot ? "restore_snapshot" : "restore_backup",
    diagnostic: health.recovery_detail?.trim() || undefined,
  };
}

export function storeIncompatibleRestartCriticalStop(): CriticalStopSpec {
  const copy = CLIENT_NOTICES.store_incompatible_restart;
  return {
    code: STORE_INCOMPATIBLE_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: copy.suggestedAction,
    recovery: "finish_restore",
  };
}

export function storeIncompatibleFreshRestartCriticalStop(): CriticalStopSpec {
  const copy = CLIENT_NOTICES.store_incompatible_fresh_restart;
  return {
    code: STORE_INCOMPATIBLE_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: copy.suggestedAction,
    recovery: "finish_fresh_start",
  };
}

export function storeIncompatibleSnapshotIncompatibleCriticalStop(): CriticalStopSpec {
  const copy = CLIENT_NOTICES.store_incompatible_snapshot_incompatible;
  return {
    code: STORE_INCOMPATIBLE_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: CLIENT_NOTICES.store_incompatible_restore_failed.message,
    recovery: "restore_backup",
  };
}

export function hostIncompatibleCriticalStop(host: HostInfo): CriticalStopSpec {
  const copy = CLIENT_NOTICES.host_incompatible;
  return {
    code: HOST_INCOMPATIBLE_STOP_CODE,
    title: copy.title,
    message: copy.message,
    retryFailed: copy.suggestedAction,
    recovery: "reconnect",
    diagnostic: `Engine contract ${host.contract_version}; this app expects ${API_CONTRACT_VERSION}.`,
  };
}

export function resolveCriticalStop(
  input: CriticalStopInput,
): CriticalStopSpec | undefined {
  // Only a person's retry starts an engine the shell stopped restarting,
  // so this stops even a window that already admitted a workspace.
  if (input.engine?.state === "stopped") {
    return engineStoppedCriticalStop(input.engine);
  }
  if (!isBackendReachable(input.sidecarStatus) && (!input.offlineAvailable || input.startFailure)) {
    return offlineCriticalStop(input.startFailure, input.startDetail);
  }

  if (input.health?.status === "recovery") {
    return storeIncompatibleCriticalStop(input.health);
  }

  if (input.host && !contractCompatible(input.host.contract_version)) {
    return hostIncompatibleCriticalStop(input.host);
  }

  const stopping = firstCatastrophicProbe(input.preflight);
  if (!stopping) return undefined;

  // Use the probe id when catalog copy has no label.
  return {
    code: stopping.code ?? stopping.id,
    title: stopping.title ?? stopping.id,
    message: stopping.message ?? "",
    detail: stopping.suggested_action,
    retryFailed: CLIENT_NOTICES.readiness_recheck_failed.message,
    recovery: "recheck",
    catastrophicDetail: stopping.catastrophic_detail,
  };
}

function firstCatastrophicProbe(
  report: PreflightReport | undefined,
): PreflightProbe | undefined {
  return report?.probes.find((probe) => probe.tier === "catastrophic");
}
