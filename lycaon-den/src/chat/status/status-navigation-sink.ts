/** Shell destinations for links inside session status chips. */
export type StatusChipNavigationTarget =
  | { kind: "settings"; section: "providers" | "approvals" }
  | { kind: "project-context"; stage: "cost" | "security" }
  | { kind: "project-configuration"; section: "trust" | "secrets" | "approvals" };

let navigate: ((target: StatusChipNavigationTarget) => void) | null = null;

export function setStatusChipNavigationSink(
  fn: ((target: StatusChipNavigationTarget) => void) | null,
): void {
  navigate = fn;
}

/** Reports whether the shell registered a navigation sink. */
export function statusChipNavigationAvailable(): boolean {
  return navigate != null;
}

export function navigateFromStatusChip(
  target: StatusChipNavigationTarget,
): void {
  navigate?.(target);
}
