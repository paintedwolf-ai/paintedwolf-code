import { createSignal } from "solid-js";
import type { EnsureNotificationPermissionResult } from "../../platform/desktop/notifications.ts";
import type { DenNotificationPrefs } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";

const DEFAULT_ON = true;

const [enabled, setEnabled] = createSignal(DEFAULT_ON);
const [finished, setFinished] = createSignal(DEFAULT_ON);
const [needsYou, setNeedsYou] = createSignal(DEFAULT_ON);
const [needsApproval, setNeedsApproval] = createSignal(DEFAULT_ON);
const [permissionResult, setPermissionResult] = createSignal<EnsureNotificationPermissionResult | null>(null);

export function notificationsEnabledPref(): boolean {
  return enabled();
}

export function notificationFinishedPref(): boolean {
  return finished();
}

export function notificationNeedsYouPref(): boolean {
  return needsYou();
}

export function notificationNeedsApprovalPref(): boolean {
  return needsApproval();
}

export function notificationPermissionResult(): EnsureNotificationPermissionResult | null {
  return permissionResult();
}

export function recordNotificationPermission(result: EnsureNotificationPermissionResult | null): void {
  setPermissionResult(result);
}

function resolveNotificationsEnabled(
  prefs?: DenNotificationPrefs,
): boolean {
  return prefs?.enabled ?? DEFAULT_ON;
}

function resolveNotificationFinished(
  prefs?: DenNotificationPrefs,
): boolean {
  return prefs?.finished ?? DEFAULT_ON;
}

function resolveNotificationNeedsYou(
  prefs?: DenNotificationPrefs,
): boolean {
  return prefs?.needsYou ?? DEFAULT_ON;
}

function resolveNotificationNeedsApproval(
  prefs?: DenNotificationPrefs,
): boolean {
  return prefs?.needsApproval ?? DEFAULT_ON;
}

/** Omitted event preferences default to enabled. */
export function resolveNotificationPrefs(
  prefs?: DenNotificationPrefs,
): Required<DenNotificationPrefs> {
  return {
    enabled: resolveNotificationsEnabled(prefs),
    finished: resolveNotificationFinished(prefs),
    needsYou: resolveNotificationNeedsYou(prefs),
    needsApproval: resolveNotificationNeedsApproval(prefs),
  };
}

export function syncNotificationPrefsFromSnapshot(): void {
  const notifications = getAppStateSnapshot().notifications;
  setEnabled(resolveNotificationsEnabled(notifications));
  setFinished(resolveNotificationFinished(notifications));
  setNeedsYou(resolveNotificationNeedsYou(notifications));
  setNeedsApproval(resolveNotificationNeedsApproval(notifications));
}

export function notificationPrefsLive(): DenNotificationPrefs {
  return {
    enabled: enabled(),
    finished: finished(),
    needsYou: needsYou(),
    needsApproval: needsApproval(),
  };
}

async function persistKey(
  key: keyof DenNotificationPrefs,
  value: boolean,
): Promise<void> {
  await persistAppStateInBackground({
    notifications: {
      ...getAppStateSnapshot().notifications,
      [key]: value,
    },
  });
}

export async function saveNotificationsEnabled(value: boolean): Promise<void> {
  setEnabled(value);
  await persistKey("enabled", value);
}

export async function saveNotificationFinished(value: boolean): Promise<void> {
  setFinished(value);
  await persistKey("finished", value);
}

export async function saveNotificationNeedsYou(value: boolean): Promise<void> {
  setNeedsYou(value);
  await persistKey("needsYou", value);
}

export async function saveNotificationNeedsApproval(
  value: boolean,
): Promise<void> {
  setNeedsApproval(value);
  await persistKey("needsApproval", value);
}
