import { resetLayoutPreferencesForTests } from "./layout-preferences.ts";
import { cancelActiveResize } from "./resize-transaction.ts";
import { resetWorkspaceLayoutForTests } from "../shell/workspace-layout.ts";
import { resetShellLayoutBusyForTests } from "../shell/shell-layout-busy.ts";

export function resetLayoutForTests(): void {
  cancelActiveResize();
  resetLayoutPreferencesForTests();
  resetWorkspaceLayoutForTests();
  resetShellLayoutBusyForTests();
}
