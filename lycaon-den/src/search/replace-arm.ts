/** The search stage consumes pending replacement intent once on mount. */

import { createSignal } from "solid-js";

export type ReplaceArmRequest = {
  originProjectId: string | null;
  query: string;
  replacement: string;
  /** Match flags to arm; omitted flags keep the user's saved prefs. */
  wholeWord?: boolean;
  caseSensitive?: boolean;
  regex?: boolean;
  /** Rename framing: the original symbol. Renders the rename banner. */
  renameFrom?: string;
};

const [pending, setPending] = createSignal<ReplaceArmRequest | null>(null);

export function armReplace(req: ReplaceArmRequest): void {
  setPending(req);
}

/** Shell routing reads pending intent without consuming it. */
export function pendingReplaceArm(): ReplaceArmRequest | null {
  return pending();
}

export function consumeReplaceArm(): ReplaceArmRequest | null {
  const v = pending();
  setPending(null);
  return v;
}

export function resetReplaceArmForTests(): void {
  setPending(null);
}
