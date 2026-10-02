import { For, Show, type JSX } from "solid-js";
import type { ManagedSecret, ManagedSecretUse } from "../../api/types.ts";
import {
  MANAGED_SECRETS_COPY as C,
  secretCustodyLabel,
  formatSecretTimestamp,
  secretAgentUseEndsLabel,
  secretLastUsedLabel,
  secretLastRevealedLabel,
  secretValueReplacedLabel,
  secretScopeHint,
  secretScopeLabel,
  secretOriginLabel,
  secretStateLabel,
  useOutcomeLabel,
} from "../../settings/security/managed-secrets-copy.ts";
import { canHold, canPromote, canReplaceValue, isRevoked, needsValue } from "../../settings/security/managed-secrets-model.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";

type SecretUses = {
  items: ManagedSecretUse[];
  loading: boolean;
  failed: boolean;
};

export type DetailMode = "facts" | "edit" | "value" | "agent_use_deadline";

type ManagedSecretDetailProps = {
  secret: ManagedSecret;
  secretId: string;
  mode: DetailMode;
  copied: boolean;
  busy: boolean;
  revealBusy: boolean;
  revealedValue?: string;
  revealSeconds: number;
  revealCopied: boolean;
  revealCopyFailed: boolean;
  uses: SecretUses;
  form: JSX.Element;
  onCopyReference: () => void;
  onReveal: () => void;
  onHideReveal: () => void;
  /** Called with the bytes actually copied; absent means copy the whole value. */
  onCopyValue: (selection?: string) => void;
  onMode: (mode: DetailMode) => void;
  /** The one-click action in flight, if any. */
  pending?: "promote" | "hold";
  onPromote: () => void;
  onHold: () => void;
  onRevoke: () => void;
};

function Fact(props: { label: string; children: JSX.Element }) {
  return (
    <div>
      <dt>{props.label}</dt>
      <dd>{props.children}</dd>
    </div>
  );
}

export function ManagedSecretDetail(props: ManagedSecretDetailProps) {
  const secret = () => props.secret;
  const editing = () => props.mode !== "facts";

  return (
    <article class="den-settings-detail" data-testid="managed-secret-card">
      <header class="den-settings-detail__header">
        <div>
          <h3>{secret().name}</h3>
          <Show when={secret().purpose?.trim()} keyed>
            {(purpose) => <p>{purpose}</p>}
          </Show>
        </div>
        <span
          class="den-settings-provider-status"
          data-configured={secret().state === "active"}
        >
          {secretStateLabel(secret())}
        </span>
      </header>

      <section class="den-secret-reference">
        <div class="den-secret-reference__head">
          <span class="den-settings-pref-label">{C.referenceLabel}</span>
          <DenButton
            variant="secondary"
            compact
            data-testid="managed-secret-copy"
            onClick={() => props.onCopyReference()}
          >
            {props.copied ? C.copiedReference : C.copyReference}
          </DenButton>
        </div>
        <code class="den-managed-secret-reference">{secret().reference}</code>
        <p class="den-settings-hint">{C.referenceHint}</p>
      </section>

      <Show when={secret().state !== "revoked"}>
        <section class="den-secret-reveal" data-testid="managed-secret-reveal">
          <div class="den-secret-reference__head">
            <span class="den-settings-pref-label">{C.revealHeading}</span>
            <Show when={props.revealedValue !== undefined}>
              <span class="den-settings-hint" role="status">
                {C.revealedHint(props.revealSeconds)}
              </span>
            </Show>
          </div>
          <Show
            when={secret().state !== "unavailable"}
            fallback={<p class="den-settings-hint">{C.revealUnavailable}</p>}
          >
            <Show
              when={props.revealedValue !== undefined}
              fallback={
                <>
                  <p class="den-settings-hint">{C.revealHint}</p>
                  <DenButton
                    variant="secondary"
                    compact
                    disabled={props.busy || props.revealBusy}
                    data-testid="managed-secret-reveal-button"
                    onClick={() => props.onReveal()}
                  >
                    {props.revealBusy ? C.revealing : C.reveal}
                  </DenButton>
                </>
              }
            >
              <Scrollport class="den-secret-reveal__value" contentClass="den-secret-reveal__content">
                <code
                  class="den-secret-reveal__code"
                  data-testid="managed-secret-revealed-value"
                  onCopy={(event) => {
                    // A partial selection copies only the selected text.
                    const selected =
                      event.currentTarget.ownerDocument.getSelection()?.toString() ?? "";
                    props.onCopyValue(selected || undefined);
                  }}
                >
                  {props.revealedValue}
                </code>
              </Scrollport>
              <div class="den-settings-provider-actions">
                <DenButton
                  variant="secondary"
                  compact
                  data-testid="managed-secret-copy-value"
                  onClick={() => props.onCopyValue()}
                >
                  {props.revealCopyFailed
                    ? C.copyValueFailed
                    : props.revealCopied
                      ? C.copiedValue
                      : C.copyValue}
                </DenButton>
                <DenButton
                  variant="secondary"
                  compact
                  data-testid="managed-secret-hide-value"
                  onClick={() => props.onHideReveal()}
                >
                  {C.hideRevealedValue}
                </DenButton>
              </div>
            </Show>
          </Show>
        </section>
      </Show>

      <dl class="den-settings-facts">
        <Fact label={C.factScope}>
          {secretScopeLabel(secret())}{" "}
          <span class="den-settings-hint">{secretScopeHint(secret())}</span>
        </Fact>
        <Fact label={C.factOrigin}>{secretOriginLabel(secret())}</Fact>
        <Fact label={C.factCustody}>{secretCustodyLabel(secret())}</Fact>
        <Show when={secret().chat_session_id}>
          <Fact label={C.factChat}>
            <Show when={secret().chat_deleted} fallback={secret().chat_title || C.chatUntitled}>
              {C.chatDeleted}{" "}
              <span class="den-settings-hint" data-testid="managed-secret-chat-deleted">
                {C.chatDeletedHint}
              </span>
            </Show>
          </Fact>
        </Show>
        <Show when={secret().format} keyed>
          {(format) => <Fact label={C.factFormat}>{format}</Fact>}
        </Show>
        <Show when={secret().entropy_bits} keyed>
          {(bits) => <Fact label={C.factEntropy}>{bits} bits</Fact>}
        </Show>
        <Fact label={C.factCreated}>
          {formatSecretTimestamp(secret().created_at) ?? "—"}
        </Fact>
        <Fact label={C.factVersion}>{C.versionLabel(secret().version)}</Fact>
        <Fact label={C.factValueReplaced}>{secretValueReplacedLabel(secret())}</Fact>
        <Fact label={C.factAgentUseEnds}>{secretAgentUseEndsLabel(secret())}</Fact>
        <Fact label={C.factState}>{secretStateLabel(secret())}</Fact>
        <Fact label={C.factLastUsed}>{secretLastUsedLabel(secret())}</Fact>
        <Fact label={C.factLastRevealed}>
          {secretLastRevealedLabel(secret())}{" "}
          <span class="den-settings-hint">{C.revealCountLabel(secret().reveal_count)}</span>
        </Fact>
      </dl>

      <section class="den-secret-uses" data-testid="managed-secret-uses">
        <div class="den-secret-reference__head">
          <span class="den-settings-pref-label">{C.usesHeading}</span>
          <span class="den-settings-hint" data-testid="managed-secret-use-count">
            {C.useCountLabel(secret().use_count)}
          </span>
        </div>
        <p class="den-settings-hint">{C.usesHint}</p>
        <Show
          when={!props.uses.loading}
          fallback={
            <p class="den-settings-hint" role="status">
              {C.usesLoading}
            </p>
          }
        >
          <Show
            when={props.uses.failed}
            fallback={
              <Show
                when={props.uses.items.length > 0}
                fallback={
                  <p class="den-settings-hint" data-testid="managed-secret-uses-empty">
                    {C.usesEmpty}
                  </p>
                }
              >
                <ul class="den-secret-use-list">
                  <For each={props.uses.items}>
                    {(use) => (
                      <li data-outcome={use.outcome} data-delivery={use.delivery}>
                        <span class="den-secret-use-list__tool">{use.tool_name}</span>
                        <span class="den-secret-use-list__outcome">
                          {useOutcomeLabel(use)}
                        </span>
                        <span class="den-secret-use-list__when">
                          {formatSecretTimestamp(use.used_at) ?? use.used_at}
                        </span>
                        <Show when={use.recipients?.length}>
                          <span class="den-secret-use-list__call">
                            {C.useRecipients((use.recipients ?? []).map((recipient) => recipient.label).join(", "))}
                          </span>
                        </Show>
                        <Show when={use.unlock_id}>
                          <span class="den-secret-use-list__call">{C.useUnlocked}</span>
                        </Show>
                        <Show when={use.tool_call_id}>
                          <span class="den-secret-use-list__call">
                            Tool call: <code>{use.tool_call_id}</code>
                          </span>
                        </Show>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>
            }
          >
            <p class="den-settings-hint" data-testid="managed-secret-uses-error">
              {C.usesError}
            </p>
          </Show>
        </Show>
      </section>

      <Show
        when={!isRevoked(secret())}
        fallback={
          <p class="den-settings-hint" data-testid="managed-secret-terminal">
            {C.revokedTerminal}
          </p>
        }
      >
        <Show when={editing()} fallback={<Actions {...props} />}>
          {props.form}
        </Show>

        <Show when={!editing()}>
          <section class="den-settings-detail__danger">
            <h4>{C.revokeHeading}</h4>
            <p class="den-settings-hint">{C.revokeHint}</p>
            <DenButton
              variant="danger"
              compact
              disabled={props.busy || props.revealBusy}
              data-testid={`managed-secret-revoke-${props.secretId}`}
              onClick={() => props.onRevoke()}
            >
              {props.busy ? C.revoking : C.revoke}
            </DenButton>
          </section>
        </Show>
      </Show>
    </article>
  );
}

function Actions(props: ManagedSecretDetailProps) {
  const secret = () => props.secret;
  return (
    <section class="den-secret-actions" data-testid="managed-secret-actions">
      <div class="den-settings-provider-actions">
        <Show when={canReplaceValue(secret())}>
          <DenButton
            variant={needsValue(secret()) ? "primary" : "secondary"}
            compact
            disabled={props.busy || props.revealBusy}
            data-testid="managed-secret-replace-value"
            onClick={() => props.onMode("value")}
          >
            {needsValue(secret()) ? C.restore : C.replace}
          </DenButton>
        </Show>
        <DenButton
          variant={secret().state === "agent_use_expired" ? "primary" : "secondary"}
          compact
          disabled={props.busy || props.revealBusy}
          data-testid="managed-secret-agent-use-deadline"
          onClick={() => props.onMode("agent_use_deadline")}
        >
          {C.agentUseDeadline}
        </DenButton>
        <DenButton
          variant="secondary"
          compact
          disabled={props.busy || props.revealBusy}
          data-testid="managed-secret-edit"
          onClick={() => props.onMode("edit")}
        >
          {C.edit}
        </DenButton>
      </div>
      <Show when={canHold(secret())}>
        <div class="den-secret-promote">
          <p class="den-settings-hint">{C.holdHint}</p>
          <DenButton
            variant="secondary"
            compact
            disabled={props.busy || props.revealBusy}
            data-testid="managed-secret-hold"
            onClick={() => props.onHold()}
          >
            {props.pending === "hold" ? C.holding : C.hold}
          </DenButton>
        </div>
      </Show>
      <Show when={canPromote(secret())}>
        <div class="den-secret-promote">
          <p class="den-settings-hint">{C.promoteHint}</p>
          <DenButton
            variant="secondary"
            compact
            disabled={props.busy || props.revealBusy}
            data-testid="managed-secret-promote"
            onClick={() => props.onPromote()}
          >
            {props.pending === "promote" ? C.promoting : C.promote}
          </DenButton>
        </div>
      </Show>
    </section>
  );
}
