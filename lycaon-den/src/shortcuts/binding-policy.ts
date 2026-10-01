import { bindingIsProducible, parseBinding } from "./chord.ts";
import {
  reservedReason,
  type ReservedReason,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";

export type BindingPolicyFailure =
  | { kind: "syntax" }
  | { kind: "reserved"; reason: ReservedReason; chord: string }
  | { kind: "unproducible" };

/** Platform policy shared by catalog defaults, loaded preferences, and saves. */
export function bindingPolicyFailure(
  binding: string,
  platform: TauriPlatform,
): BindingPolicyFailure | null {
  const parsed = parseBinding(binding);
  if (!parsed) return { kind: "syntax" };
  const steps =
    parsed.kind === "chord"
      ? [parsed.chord]
      : [parsed.leader, parsed.secondKey];
  for (const chord of steps) {
    const reason = reservedReason(chord, platform);
    if (reason) return { kind: "reserved", reason, chord };
  }
  if (!bindingIsProducible(binding, platform)) {
    return { kind: "unproducible" };
  }
  return null;
}

export function bindingAllowedOnPlatform(
  binding: string,
  platform: TauriPlatform,
): boolean {
  return bindingPolicyFailure(binding, platform) === null;
}

/** Host defaults may claim standard app chords, never OS-consumed or assistive ones. */
export function defaultBindingAllowedOnPlatform(
  binding: string,
  platform: TauriPlatform,
): boolean {
  const parsed = parseBinding(binding);
  if (!parsed || !bindingIsProducible(binding, platform)) return false;
  const steps =
    parsed.kind === "chord"
      ? [parsed.chord]
      : [parsed.leader, parsed.secondKey];
  return steps.every((chord) => {
    const reason = reservedReason(chord, platform);
    return reason !== "assistive" && reason !== "os";
  });
}
