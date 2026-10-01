import { runShellLayoutTransaction } from "./shell-layout-busy.ts";

type PaneVisibilityDeps = {
  /** Whether the pane is painted closed. */
  collapsed: () => boolean;
  /** Claims the window width an open pane needs. The pane paints either way. */
  widen: () => Promise<unknown>;
  paintOpen: () => void;
  paintClosed: () => void;
};

/** Repeat presses use the requested visibility while the window resizes. */
export function createPaneVisibilityIntent(deps: PaneVisibilityDeps) {
  let restore: { token: object; opened: Promise<boolean> } | null = null;

  const intendsOpen = () => restore != null || !deps.collapsed();

  /** Resolves true when the pane painted open, false when a hide overtook it. */
  const show = (): Promise<boolean> => {
    if (restore) return restore.opened;
    const token = {};
    const opened = runShellLayoutTransaction(async () => {
      // Resize before painting the pane to avoid two layout passes.
      await deps.widen();
      if (restore?.token !== token) return false;
      deps.paintOpen();
      return true;
    });
    restore = { token, opened };
    const settle = () => {
      if (restore?.token === token) restore = null;
    };
    void opened.then(settle, settle);
    return opened;
  };

  const hide = () => {
    restore = null;
    deps.paintClosed();
  };

  const toggle = () => {
    if (intendsOpen()) hide();
    else void show();
  };

  return { show, hide, toggle, intendsOpen };
}
