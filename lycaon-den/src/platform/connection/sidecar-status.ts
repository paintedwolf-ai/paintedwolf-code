import type { SidecarStatus } from "../../store/app-state-model.ts";

/** Sidecar SSE has completed at least one handshake. */
export function isSidecarEstablished(status: SidecarStatus): boolean {
  return status === "connected" || status === "reconnecting";
}

/**
 * Sidecar is reachable for HTTP calls and live chat chrome.
 * Includes first-connect "connecting" so boot/resubscribe handshakes do not flash offline.
 */
export function isBackendReachable(status: SidecarStatus): boolean {
  return isSidecarEstablished(status) || status === "connecting";
}
