import type { ApprovalGate } from "../../api/types.ts";
import { GATE_LABELS } from "./gate-copy.generated.ts";

/** The generated label table covers every host gate. */
export function gateLabel(gate: ApprovalGate | undefined): string {
  return gate ? GATE_LABELS[gate] : "";
}
