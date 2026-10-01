import type { SplitPane } from "../../shared/app-state-types.ts";
import { runShellLayoutTransaction } from "./shell-layout-busy.ts";

type Dependencies = {
  hidden: () => SplitPane | null;
  commit: (pane: SplitPane | null) => void;
  widen: () => Promise<unknown>;
  onHide: (pane: SplitPane) => void;
};

/** One pending restore and one hidden pane prevent contradictory split intents. */
export function createSplitPaneVisibility(deps: Dependencies) {
  let restore: { pane: SplitPane; token: object; opened: Promise<boolean> } | null = null;

  const show = (pane: SplitPane): Promise<boolean> => {
    if (restore?.pane === pane) return restore.opened;
    if (deps.hidden() !== pane) return Promise.resolve(true);
    const token = {};
    const opened = runShellLayoutTransaction(async () => {
      await deps.widen();
      if (restore?.token !== token) return false;
      if (deps.hidden() === pane) deps.commit(null);
      return true;
    });
    restore = { pane, token, opened };
    const settle = () => { if (restore?.token === token) restore = null; };
    void opened.then(settle, settle);
    return opened;
  };

  const hide = (pane: SplitPane) => {
    restore = null;
    deps.commit(pane);
    deps.onHide(pane);
  };

  const toggle = (pane: SplitPane) => {
    if (restore?.pane === pane || deps.hidden() !== pane) hide(pane);
    else void show(pane);
  };

  return { show, hide, toggle };
}
