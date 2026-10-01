import { createSignal } from "solid-js";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import type { NoticeInput } from "../../notices/notice-model.ts";
import type { HealthResponse } from "./backend.ts";
import { DEN_VERSION } from "../desktop/den-version.ts";
import { compare as compareVersions, valid as validVersion } from "semver";

let lastStoreRevision: number | null = null;
let lastSchemaVersion: number | null = null;
let lastHostVersion: string | null = null;
let denSkewNoticePublished = false;

const [lastHealth, setLastHealth] = createSignal<HealthResponse | null>(null);

export function isBackendUsable(health: HealthResponse | null | undefined): boolean {
  return health?.status === "ok";
}

export function lastSeenHealth(): HealthResponse | null {
  return lastHealth();
}

export function noteStoreRevision(revision: number): boolean {
  const prev = lastStoreRevision;
  lastStoreRevision = revision;
  return prev !== null && prev !== revision;
}

export function resetStoreRevisionTracking(): void {
  lastStoreRevision = null;
  lastSchemaVersion = null;
  lastHostVersion = null;
  setLastHealth(null);
  denSkewNoticePublished = false;
}

export function lastSeenStoreRevision(): number | null {
  return lastStoreRevision;
}

export function lastSeenHostVersion(): string | null {
  return lastHostVersion;
}

export function lastSeenSchemaVersion(): number | null {
  return lastSchemaVersion;
}

/** Malformed versions sort below valid versions. */
export function compareSemver(a: string, b: string): number {
  const left = a;
  const right = b;
  const leftValid = left === left.trim() && !left.startsWith("v")
    ? validVersion(left)
    : null;
  const rightValid = right === right.trim() && !right.startsWith("v")
    ? validVersion(right)
    : null;
  if (!leftValid || !rightValid) {
    if (leftValid) return 1;
    if (rightValid) return -1;
    return left.localeCompare(right);
  }
  return compareVersions(leftValid, rightValid);
}

export function denVersionIsOlderThanMin(
  minDenVersion: string | undefined,
  denVersion: string = DEN_VERSION,
): boolean {
  const min = minDenVersion?.trim();
  if (!min) return false;
  return compareSemver(denVersion, min) < 0;
}

/** Records health and returns one skew notice. */
export function noteHealthResponse(
  health: HealthResponse,
): NoticeInput | null {
  setLastHealth(health);
  if (typeof health.schema_version === "number") {
    lastSchemaVersion = health.schema_version;
  }
  if (typeof health.version === "string" && health.version.trim() !== "") {
    lastHostVersion = health.version;
  }
  // Recovery suppresses the softer skew notice.
  if (!isBackendUsable(health)) {
    return null;
  }
  if (!denVersionIsOlderThanMin(health.min_den_version)) {
    return null;
  }
  if (denSkewNoticePublished) {
    return null;
  }
  denSkewNoticePublished = true;
  const copy = CLIENT_NOTICES.den_version_skew;
  return {
    severity: "warning",
    code: "den_version_skew",
    title: copy.title,
    message: copy.message,
  };
}
