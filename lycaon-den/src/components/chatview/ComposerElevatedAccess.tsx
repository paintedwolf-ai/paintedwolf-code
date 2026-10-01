import { createComputed, createEffect, createMemo, onCleanup, Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { navigateFromStatusChip, statusChipNavigationAvailable } from "../../chat/status/status-navigation-sink.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

const copy = APPROVALS_COPY.elevated;

type Options = {
  client: () => LycaonClient | null;
  connected: () => boolean;
  sessionId: () => string;
  revision: () => string;
  focus: () => void;
};

/** The composer advertises live elevated access; revocation lives in Approvals. */
export function createComposerElevatedAccess(options: Options) {
  const live = useResidentLive();
  const interactive = useResidentInteractive();
  const source = createMemo<{ client: LycaonClient; sessionId: string; key: string } | null>((previous) => {
    const sessionId = options.sessionId();
    const client = options.client() ?? (previous?.sessionId === sessionId ? previous.client : null);
    return client && sessionId ? { client, sessionId, key: `${sessionId}:${options.revision()}` } : null;
  });
  const query = createSurfaceQuery({
    name: "elevated-access",
    source,
    scope: (target) => target.sessionId,
    required: false,
    load: ({ client, sessionId }) => options.connected()
      ? client.getElevatedAccess(sessionId)
      : Promise.reject(new Error(copy.unavailable)),
  });
  createEffect(() => {
    const summary = query.value();
    if (!live() || !summary?.approvals_enabled || !options.connected()) return;
    const expiries = summary.records.flatMap((row) => row.expires_at ? [Date.parse(row.expires_at)] : []).filter(Number.isFinite);
    if (!expiries.length) return;
    const timer = setTimeout(() => void query.refresh(), Math.min(2_147_483_647, Math.max(100, Math.min(...expiries) - Date.now() + 50)));
    onCleanup(() => clearTimeout(timer));
  });
  const visible = () => query.value()?.approvals_enabled === true && (query.value()?.total ?? 0) > 0;
  let button: HTMLButtonElement | undefined;
  let wasVisible = false;
  createComputed(() => {
    const showing = visible();
    if (interactive() && wasVisible && !showing && document.activeElement === button) options.focus();
    wasVisible = showing;
  });
  const disabled = () => !interactive() || !options.connected() || query.loading() || !!query.error() || !statusChipNavigationAvailable();
  const label = () => !options.connected() ? copy.unavailable
    : query.error() ? copy.refreshFailed : query.loading() ? copy.loading : copy.action(query.value()?.total ?? 0);
  const tooltip = () => disabled() ? label() : copy.tooltip(query.value()?.total ?? 0);
  const openApprovals = () => {
    if (!visible() || disabled()) return;
    navigateFromStatusChip({ kind: "project-configuration", section: "approvals" });
  };
  return { visible, disabled, label, tooltip, openApprovals, query, attachButton: (element: HTMLButtonElement) => { button = element; } };
}

type State = ReturnType<typeof createComposerElevatedAccess>;

export function ElevatedAccessButton(props: { state: State }) {
  return <Show when={props.state.visible()}>
    <div class="den-composer-status-item" data-status-id="elevated-access">
      <button ref={props.state.attachButton} type="button" class="den-composer-status-button den-composer-elevated-access"
        data-testid="composer-elevated-access" aria-label={props.state.label()} data-tip={props.state.tooltip()}
        disabled={props.state.disabled()} onClick={props.state.openApprovals}>
        <ThemeIcon slot="elevated-access" size={14} />
      </button>
    </div>
  </Show>;
}
