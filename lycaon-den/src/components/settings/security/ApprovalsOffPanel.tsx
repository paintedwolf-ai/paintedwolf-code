import { For, Show, createEffect, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import {
  DEFAULT_NEVER_ASK,
  NEVER_ASK_COPY,
} from "../../../settings/security/approvals-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

type Props = {
  client: LycaonClient | null | undefined;
};

export function ApprovalsOffPanel(props: Props) {
  const [neverAsk, setNeverAsk] = createSignal(DEFAULT_NEVER_ASK);
  const [confirming, setConfirming] = createSignal(false);
  const [acknowledged, setAcknowledged] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal<string | undefined>();

  const reload = async () => {
    const c = props.client;
    if (!c) return;
    try {
      const cfg = await c.getApprovalsSettings();
      setNeverAsk(cfg.never_ask ?? DEFAULT_NEVER_ASK);
    } catch {
      // A failed read shows never-ask as off.
      setNeverAsk(DEFAULT_NEVER_ASK);
    }
  };

  createEffect(() => {
    props.client;
    void reload();
  });

  const apply = async (next: boolean) => {
    const c = props.client;
    if (!c) return;
    setBusy(true);
    setError(undefined);
    try {
      const cfg = await c.updateApprovalsSettings({ never_ask: next });
      setNeverAsk(cfg.never_ask ?? next);
      setConfirming(false);
      setAcknowledged(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : NEVER_ASK_COPY.saveError);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      class="den-settings-section"
      data-testid="approvals-off-panel"
      {...settingAnchor("never-ask")}
    >
      <h3 class="den-settings-subhead">{settingLabel("never-ask")}</h3>

      <Show when={!neverAsk()}>
        <p class="den-settings-hint">{NEVER_ASK_COPY.hint}</p>

        <Show
          when={confirming()}
          fallback={
            <div class="den-settings-actions">
              <DenButton
                variant="secondary"
                disabled={busy()}
                data-testid="approvals-off-start"
                onClick={() => setConfirming(true)}
              >
                {NEVER_ASK_COPY.label}
              </DenButton>
            </div>
          }
        >
          <div
            class="den-settings-subsection"
            data-testid="approvals-off-confirm"
          >
            <p class="den-settings-warn">{NEVER_ASK_COPY.dangerTitle}</p>
            <For each={NEVER_ASK_COPY.dangerBody}>
              {(line) => <p class="den-settings-hint">{line}</p>}
            </For>

            <DenCheckbox
              checked={acknowledged()}
              disabled={busy()}
              data-testid="approvals-off-acknowledge"
              onChange={(e) => setAcknowledged(e.currentTarget.checked)}
            >
              {NEVER_ASK_COPY.acknowledgeLabel}
            </DenCheckbox>

            <div class="den-settings-actions">
              <DenButton
                variant="danger"
                disabled={!acknowledged() || busy()}
                data-testid="approvals-off-confirm-button"
                onClick={() => void apply(true)}
              >
                {NEVER_ASK_COPY.confirm}
              </DenButton>
              <DenButton
                variant="secondary"
                disabled={busy()}
                data-testid="approvals-off-cancel"
                onClick={() => {
                  setConfirming(false);
                  setAcknowledged(false);
                }}
              >
                {NEVER_ASK_COPY.cancel}
              </DenButton>
            </div>
          </div>
        </Show>
      </Show>

      <Show when={neverAsk()}>
        <div
          class="den-settings-subsection"
          data-testid="approvals-off-active"
        >
          <p class="den-settings-warn">{NEVER_ASK_COPY.enabledNotice}</p>
          <div class="den-settings-actions">
            <DenButton
              variant="primary"
              disabled={busy()}
              data-testid="approvals-off-restore"
              onClick={() => void apply(false)}
            >
              {NEVER_ASK_COPY.turnBackOn}
            </DenButton>
          </div>
        </div>
      </Show>

      <Show when={error()}>
        <p
          class="den-settings-warn"
          role="alert"
          data-testid="approvals-off-error"
        >
          {error()}
        </p>
      </Show>
    </section>
  );
}
