import { For, Show, createMemo, createSignal, onMount } from "solid-js";
import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { attachmentKindGlyph } from "../../chat/composer/composer-attachments.ts";
import type { LaunchChip } from "../../home/launch/home-launch-model.ts";
import { homeLaunches } from "../../home/launch/home-launches.ts";
import { launchNotice, launchStepText, saveNotice, type LaunchAction, type LaunchNotice } from "../../home/launch/launch-status.ts";
import type { HomeLaunchController } from "../shell/home-launch-controller.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { UserBubble } from "../transcript/UserBubble.tsx";

type Props = {
  launchId: string;
  controller: HomeLaunchController;
  onOpenProviders: () => void;
};

function chipProblem(chip: LaunchChip): string | undefined {
  if (chip.kind === "reject") return chip.reason;
  if (chip.snapshot === "missing") return "This file is no longer available. Choose it again.";
  if (chip.snapshot === "changed") return "This file changed after you attached it.";
  return undefined;
}

/**
 * A submitted Home idea shown as its conversation until the host binds and
 * admits its first turn. Every wait, error and recovery for the submission
 * lives here.
 */
export function LaunchConversation(props: Props) {
  const record = () => homeLaunches.record(props.launchId);
  const runtime = () => homeLaunches.runtime(props.launchId);
  const [editing, setEditing] = createSignal(false);
  const [draft, setDraft] = createSignal("");
  let regionRef: HTMLElement | undefined;
  let fileInput: HTMLInputElement | undefined;
  let replacing: string | undefined;

  onMount(() => regionRef?.focus({ preventScroll: true }));

  const pending = createMemo((): PendingSend | undefined => {
    const current = record();
    if (!current) return undefined;
    return {
      kind: "prompt",
      operationId: current.promptOperationId,
      text: current.proposedText ?? current.text,
      createdAt: current.createdAt,
      attachmentLabels: current.chips.filter((chip) => !chipProblem(chip)).map((chip) => chip.name),
      state: "sending",
    };
  });
  const troubled = () => (record()?.chips ?? []).filter((chip) => chipProblem(chip));
  const notice = (): LaunchNotice | undefined => {
    const current = record();
    const blocker = runtime()?.blocker;
    return current && blocker ? launchNotice(current, blocker) : undefined;
  };
  const save = () => {
    const state = runtime();
    return state ? saveNotice(state) : undefined;
  };
  const active = () => record()?.intent === "send" && runtime()?.blocker?.code !== "stopped";
  const statusText = () => {
    const state = runtime();
    if (!state || state.blocker || !state.slow) return undefined;
    return launchStepText(state.step);
  };

  const run = (action: LaunchAction) => {
    const id = props.launchId;
    switch (action) {
      case "retry": props.controller.retry(id); break;
      case "retry_saving": props.controller.retrySaving(id); break;
      case "copy": void navigator.clipboard?.writeText(record()?.proposedText ?? record()?.text ?? ""); break;
      case "send_here": props.controller.sendHere(id); break;
      case "open_settings": props.onOpenProviders(); break;
      case "open_chat": props.controller.openChat(id); break;
      case "edit_on_home": props.controller.editOnHome(id); break;
      case "start_again": props.controller.startAgain(id); break;
      case "remove": props.controller.discard(id); break;
    }
  };

  const NoticeCard = (card: { notice: LaunchNotice; testId: string }) => (
    <section class="den-launch-notice" data-tone={card.notice.tone} data-testid={card.testId}>
      <div class="den-launch-notice__text">
        <span class="den-launch-notice__title">{card.notice.title}</span>
        <Show when={card.notice.body}>
          <span class="den-launch-notice__body">{card.notice.body}</span>
        </Show>
      </div>
      <div class="den-launch-notice__actions">
        <Show when={card.notice.secondary} keyed>
          {(secondary) => (
            <DenButton variant="secondary" compact data-testid={`launch-action-${secondary.action}`} onClick={() => run(secondary.action)}>
              {secondary.label}
            </DenButton>
          )}
        </Show>
        <Show when={card.notice.primary} keyed>
          {(primary) => (
            <DenButton variant="primary" compact data-testid={`launch-action-${primary.action}`} onClick={() => run(primary.action)}>
              {primary.label}
            </DenButton>
          )}
        </Show>
      </div>
    </section>
  );

  return (
    <section
      ref={regionRef}
      class="den-launch"
      tabindex="-1"
      aria-label="New chat"
      data-testid="launch-conversation"
      data-launch-id={props.launchId}
    >
      <Show when={record()}>
        <Scrollport class="den-launch__scroll" contentClass="den-launch__stream">
          <Show
            when={!editing()}
            fallback={
              <div class="den-launch__edit">
                <textarea
                  class="den-launch__edit-input"
                  aria-label="Edit your idea"
                  data-testid="launch-edit-input"
                  value={draft()}
                  onInput={(e) => setDraft(e.currentTarget.value)}
                />
                <div class="den-launch__edit-actions">
                  <DenButton variant="secondary" compact onClick={() => setEditing(false)}>Cancel</DenButton>
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="launch-edit-save"
                    disabled={!draft().trim()}
                    onClick={() => {
                      props.controller.edit(props.launchId, draft().trim());
                      setEditing(false);
                    }}
                  >
                    Save
                  </DenButton>
                </div>
              </div>
            }
          >
            <UserBubble
              content={pending()?.text ?? ""}
              pending={pending()}
              entryKey={props.launchId}
              rowId={record()?.promptOperationId}
              reserveActions
              ts={new Date(record()?.createdAt ?? Date.now()).toISOString()}
            />
          </Show>
          <Show when={troubled().length > 0}>
            <ul class="den-launch__chips" data-testid="launch-chip-problems">
              <For each={troubled()}>
                {(chip) => (
                  <li class="den-launch__chip" data-testid="launch-chip-problem" data-item-id={chip.itemId}>
                    <span class="den-launch__chip-glyph" aria-hidden="true">{attachmentKindGlyph("reject")}</span>
                    <span class="den-launch__chip-name">{chip.name}</span>
                    <span class="den-launch__chip-reason">{chipProblem(chip)}</span>
                    <span class="den-launch__chip-actions">
                      <Show when={chip.kind === "path" && chip.snapshot === "changed"}>
                        <DenButton variant="secondary" compact onClick={() => props.controller.acceptChangedFile(props.launchId, chip.itemId)}>
                          Use the changed file
                        </DenButton>
                      </Show>
                      <Show when={chip.kind !== "reject"}>
                        <DenButton
                          variant="secondary"
                          compact
                          onClick={() => { replacing = chip.itemId; fileInput?.click(); }}
                        >
                          Choose again
                        </DenButton>
                      </Show>
                      <DenButton
                        variant="link"
                        compact
                        data-testid="launch-chip-remove"
                        onClick={() => props.controller.removeChip(props.launchId, chip.itemId)}
                      >
                        Remove
                      </DenButton>
                    </span>
                  </li>
                )}
              </For>
            </ul>
          </Show>
          <Show when={save()} keyed>
            {(card) => <NoticeCard notice={card} testId="launch-save-notice" />}
          </Show>
          <Show when={notice()} keyed>
            {(card) => <NoticeCard notice={card} testId="launch-notice" />}
          </Show>
          <p class="den-launch__status" role="status" data-testid="launch-status">
            {statusText() ?? ""}
          </p>
        </Scrollport>
        <footer class="den-launch__footer">
          <Show when={active() && !editing()}>
            <DenButton
              variant="link"
              compact
              data-testid="launch-edit"
              onClick={() => { setDraft(record()?.proposedText ?? record()?.text ?? ""); setEditing(true); }}
            >
              Edit
            </DenButton>
            <DenButton variant="secondary" compact data-testid="launch-stop" onClick={() => props.controller.stop(props.launchId)}>
              Stop
            </DenButton>
          </Show>
        </footer>
        <input
          ref={fileInput}
          type="file"
          class="sr-only"
          tabindex="-1"
          aria-hidden="true"
          onChange={(e) => {
            const file = e.currentTarget.files?.[0];
            e.currentTarget.value = "";
            if (file && replacing) props.controller.replaceChip(props.launchId, replacing, file);
            replacing = undefined;
          }}
        />
      </Show>
    </section>
  );
}
