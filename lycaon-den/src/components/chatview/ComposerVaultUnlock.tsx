import { createEffect, createMemo, createSignal, onCleanup, Show } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { applyChatVault, chatVault } from "../../chat/vault/chat-vault-store.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

const copy = APPROVALS_COPY.vault;

type Options = {
  client: () => LycaonClient | null;
  connected: () => boolean;
  sessionId: () => string;
  revision: () => string;
};

function clockTime(iso: string | undefined): string {
  const at = iso ? Date.parse(iso) : Number.NaN;
  return Number.isFinite(at)
    ? new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : "";
}

/**
 * The composer shows when this chat is unlocked for values the person
 * stored, and locks it on request. `chat_vault` events keep it current; a
 * read on connect fills it in.
 */
export function createComposerVaultUnlock(options: Options) {
  const interactive = useResidentInteractive();
  const source = createMemo(() => {
    const client = options.client();
    const sessionId = options.sessionId();
    return client && sessionId && options.connected()
      ? { client, sessionId, key: `${sessionId}:${options.revision()}` }
      : null;
  });
  const query = createSurfaceQuery({
    name: "chat-vault",
    source,
    scope: (target) => target.sessionId,
    required: false,
    load: ({ client, sessionId }) => client.getChatVault(sessionId),
  });
  createEffect(() => {
    const state = query.value();
    if (state) applyChatVault(state);
  });
  const state = () => chatVault(options.sessionId());
  // The engine announces an idle or ceiling end within its sweep; reading
  // at the deadline keeps the chip from outliving the unlock.
  createEffect(() => {
    const closes = Date.parse(state()?.closes_at ?? "");
    if (!state()?.unlocked || !Number.isFinite(closes)) return;
    const timer = setTimeout(() => void query.refresh(), Math.min(2_147_483_647, Math.max(100, closes - Date.now() + 50)));
    onCleanup(() => clearTimeout(timer));
  });
  const [locking, setLocking] = createSignal(false);
  const [failed, setFailed] = createSignal(false);
  const visible = () => state()?.unlocked === true;
  const until = () => clockTime(state()?.closes_at);
  const disabled = () => !interactive() || !options.connected() || locking();
  const label = () => failed() ? copy.lockFailed : copy.label(until());
  const tooltip = () => failed() ? copy.lockFailed : copy.tooltip(until());
  const lock = async () => {
    const client = options.client();
    const sessionId = options.sessionId();
    if (!client || !sessionId || disabled()) return;
    setLocking(true);
    setFailed(false);
    try {
      applyChatVault(await client.lockChatVault(sessionId));
    } catch {
      setFailed(true);
    } finally {
      setLocking(false);
    }
  };
  return { visible, disabled, label, tooltip, lock };
}

type State = ReturnType<typeof createComposerVaultUnlock>;

export function VaultUnlockButton(props: { state: State }) {
  return <Show when={props.state.visible()}>
    <div class="den-composer-status-item" data-status-id="chat-vault">
      <button type="button" class="den-composer-status-button den-composer-vault-unlock"
        data-testid="composer-vault-unlock" aria-label={props.state.label()} data-tip={props.state.tooltip()}
        disabled={props.state.disabled()} onClick={() => void props.state.lock()}>
        <ThemeIcon slot="settings-secrets" size={14} />
      </button>
    </div>
  </Show>;
}
