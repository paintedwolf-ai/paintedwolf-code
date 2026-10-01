import { APP_SCOPE } from "../../notices/notice-scope.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import {
  nativeUpdateService,
  type NativeUpdateState,
  type UpdateService,
} from "./update-service.ts";

type MountUpdateNoticeOptions = {
  notices: NoticeStore;
  service?: UpdateService;
};

export function mountUpdateNotice(options: MountUpdateNoticeOptions): () => void {
  const service = options.service ?? nativeUpdateService;
  let disposed = false;
  let unlisten = () => {};
  let revision = 0;
  let publishedVersion: string | null = null;

  const observe = (state: NativeUpdateState) => {
    if (disposed || state.revision < revision) return;
    revision = state.revision;
    if (
      state.phase !== "available" ||
      !state.available_version ||
      state.available_version === publishedVersion
    ) {
      return;
    }
    publishedVersion = state.available_version;
    const copy = CLIENT_NOTICES.update_available;
    options.notices.publish(
      {
        severity: "info",
        code: "update_available",
        title: copy.title,
        message: copy.message,
        suggestedAction: copy.suggestedAction,
        actions: copy.action ? [copy.action] : undefined,
      },
      APP_SCOPE,
    );
  };

  void (async () => {
    try {
      const stop = await service.subscribe(observe);
      if (disposed) {
        stop();
        return;
      }
      unlisten = stop;
    } catch {
      // The initial read can still discover an update.
    }
    if (!disposed) await service.getState().then(observe).catch(() => undefined);
  })();

  return () => {
    disposed = true;
    unlisten();
  };
}
