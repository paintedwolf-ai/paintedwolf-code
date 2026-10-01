/** Connected host identity, caller, and capabilities. */

import { createSignal } from "solid-js";
import { major as semverMajor, valid as validSemver } from "semver";
import { API_CONTRACT_VERSION } from "../../api/operations.generated.ts";
import type { HostCapability, HostInfo } from "../../api/types.ts";

const [hostInfo, setHostInfo] = createSignal<HostInfo | null>(null);

// Kept across disconnects so reconnecting to a different install is recognized.
let lastHostId: string | null = null;

export type HostInfoNote = {
  /** The connected installation changed across reconnects. */
  hostChanged: boolean;
  compatible: boolean;
};

/** Last handshake read from the connected host. */
export function hostIdentity(): HostInfo | null {
  return hostInfo();
}

/** True only when `personId` names the authenticated caller. */
export function isCallerPerson(personId: string | null | undefined): boolean {
  const caller = hostInfo()?.caller.id;
  return Boolean(caller) && personId === caller;
}

function hostHasCapability(capability: HostCapability): boolean {
  return hostInfo()?.capabilities.includes(capability) ?? false;
}

/** Host paths, the file manager, native pickers, and local editors require this. */
export function hostSharesDevice(): boolean {
  return hostHasCapability("shared_device");
}

export function contractMajor(version: string): number | null {
  if (version !== version.trim() || !/^[0-9]/.test(version)) return null;
  const parsed = validSemver(version);
  return parsed ? semverMajor(parsed) : null;
}

export function contractCompatible(
  hostContract: string,
  clientContract: string = API_CONTRACT_VERSION,
): boolean {
  const host = contractMajor(hostContract);
  return host !== null && host === contractMajor(clientContract);
}

/** Connected host with an incompatible contract version. */
export function incompatibleHost(): HostInfo | null {
  const info = hostInfo();
  return info && !contractCompatible(info.contract_version) ? info : null;
}

export function noteHostInfo(info: HostInfo): HostInfoNote {
  const hostChanged = lastHostId !== null && lastHostId !== info.host_id;
  lastHostId = info.host_id;
  setHostInfo(info);
  return { hostChanged, compatible: contractCompatible(info.contract_version) };
}

export function resetHostIdentityForTest(info: HostInfo | null = null): void {
  lastHostId = info?.host_id ?? null;
  setHostInfo(info);
}
