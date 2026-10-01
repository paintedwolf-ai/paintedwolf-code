import { createSignal } from "solid-js";
import type { FirstTimeTipId } from "./first-time-tips-catalog.ts";

const [requested, setRequested] = createSignal<readonly FirstTimeTipId[]>([]);

export function requestedFirstTimeTips(): readonly FirstTimeTipId[] {
  return requested();
}

export function requestFirstTimeTip(id: FirstTimeTipId): void {
  setRequested((current) => (current.includes(id) ? current : [...current, id]));
}

export function clearFirstTimeTipRequest(id: FirstTimeTipId): void {
  setRequested((current) => current.filter((currentId) => currentId !== id));
}

export function resetFirstTimeTipRequestsForTests(): void {
  setRequested([]);
}
