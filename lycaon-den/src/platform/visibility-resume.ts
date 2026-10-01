/** When the window regains focus during SSE reconnect, retry immediately. */
export function watchDocumentVisibilityResume(opts: {
  isReconnecting: () => boolean;
  wakeReconnect: () => void;
  onVisible?: () => void;
}): () => void {
  if (typeof document === "undefined") return () => undefined;

  const onVisibility = () => {
    if (document.visibilityState !== "visible") return;
    opts.onVisible?.();
    if (!opts.isReconnecting()) return;
    opts.wakeReconnect();
  };

  document.addEventListener("visibilitychange", onVisibility);
  return () => document.removeEventListener("visibilitychange", onVisibility);
}
