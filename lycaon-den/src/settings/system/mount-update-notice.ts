import { createEffect, createRoot, on, onCleanup } from "solid-js";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { nativeUpdateState, type createUpdateState } from "./update-state.ts";
import type { NativeUpdateState } from "./update-service.ts";

const codes = ["update_available", "update_ready", "update_failed"] as const;
type UpdateNotice = (typeof codes)[number];

function noticeFor(state: NativeUpdateState | null): UpdateNotice | undefined {
  if (!state) return undefined;
  if (state.capabilities.can_restart_to_update) return "update_ready";
  if (state.installation === "failed" && state.last_error && state.last_error.code !== "check_failed" && state.last_error.code !== "cancelled") return "update_failed";
  if (!state.automatic_updates_enabled && state.capabilities.can_download && state.candidate?.rollout_eligibility === "eligible") return "update_available";
  return undefined;
}

export function mountUpdateNotice(options: {
  notices: NoticeStore;
  updates?: ReturnType<typeof createUpdateState>;
}): () => void {
  const updates = options.updates ?? nativeUpdateState;
  return createRoot((dispose) => {
    onCleanup(updates.mount());
    let published: string | undefined;
    createEffect(on(updates.state, (state) => {
      const code = noticeFor(state);
      const identity = code ? `${state?.candidate?.release_id}:${code}` : undefined;
      if (identity === published) return;
      published = identity;
      for (const previous of codes) options.notices.withdraw(previous, APP_SCOPE);
      if (!code) return;
      const copy = CLIENT_NOTICES[code];
      options.notices.publish({
        code, severity: code === "update_failed" ? "warning" : "info",
        title: copy.title, message: copy.message, suggestedAction: copy.suggestedAction,
      }, APP_SCOPE);
    }));
    return dispose;
  });
}
