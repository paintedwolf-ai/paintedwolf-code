import { createEffect, createRoot, on, onCleanup } from "solid-js";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { nativeUpdateState, type createUpdateState } from "./update-state.ts";
import type { NativeUpdateState } from "./update-service.ts";

const codes = ["update_available", "update_ready", "update_failed", "update_feed_rejected"] as const;
type UpdateNotice = (typeof codes)[number];

function noticeFor(state: NativeUpdateState | null): UpdateNotice | undefined {
  if (!state || state.capabilities.blocked_reason === "development_build") return undefined;
  if (state.last_error?.code === "feed_rejected") return "update_feed_rejected";
  if (state.capabilities.can_restart_to_update && state.offer_confirmed_at !== null) return "update_ready";
  if (state.installation === "failed" && state.last_error && state.last_error.code !== "check_failed" && state.last_error.code !== "cancelled") return "update_failed";
  if (state.candidate?.rollout_eligibility !== "eligible") return undefined;
  // The upstream release notice does not assert package-source availability.
  if (state.install_source === "system_package") return "update_available";
  if (!state.automatic_updates_enabled && state.capabilities.can_download) return "update_available";
  return undefined;
}

// A notice is about one release: the staged or committed one when there is one, else the offer.
function noticeIdentity(state: NativeUpdateState | null, code: UpdateNotice): string {
  return `${state?.staged_release_id ?? state?.candidate?.release_id}:${code}`;
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
      const identity = code ? noticeIdentity(state, code) : undefined;
      if (identity === published) return;
      published = identity;
      for (const previous of codes) options.notices.withdraw(previous, APP_SCOPE);
      if (!code) return;
      const copy = CLIENT_NOTICES[code];
      options.notices.publish({
        code, severity: code === "update_failed" || code === "update_feed_rejected" ? "warning" : "info",
        title: copy.title, message: copy.message, suggestedAction: copy.suggestedAction,
      }, APP_SCOPE);
    }));
    return dispose;
  });
}
