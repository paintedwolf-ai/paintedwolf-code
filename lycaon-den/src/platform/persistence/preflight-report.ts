import { createSignal } from "solid-js";
import type { PreflightReport } from "../../api/types.ts";

const [report, setReport] = createSignal<PreflightReport | undefined>();

/** Undefined means readiness has not been read. */
export function preflightReport(): PreflightReport | undefined {
  return report();
}

/** The refresh path publishes each host answer here. */
export function setPreflightReport(next: PreflightReport | undefined): void {
  setReport(next);
}
