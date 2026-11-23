import { useNoticesOptional } from "../../notices/notice-reporter.tsx";
import { For, Show, createMemo, createSignal } from "solid-js";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import type { HostSecretRedactionMeta, MessageContentPart, ViewContext } from "../../api/types.ts";
import { attachmentKindGlyph } from "../../chat/composer/composer-attachments.ts";
import { REDACTION_FIELD_CONTENT } from "../../chat/transcript/content/redaction-spans.ts";
import { RedactedText } from "./RedactionMark.tsx";
import {
  projectUserMessageParts,
  transcriptChipTarget,
  type TranscriptAttachmentChip,
} from "../../chat/transcript/content/user-message-parts.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { markdownProjectPathLinkContextMenu } from "../../chat/markdown/markdown-project-path-link.ts";
import { ContextMenu } from "../ContextMenu.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { MessageActions, buildUserMessageActions } from "./MessageActions.tsx";
import { VisualPresentBlock } from "./VisualPresentBlock.tsx";
import { ViewContextLine } from "./ViewContextLine.tsx";
import { messageTimesPref } from "../../settings/chat/chat-prefs.ts";
import { MessageTime } from "./TranscriptTimeRows.tsx";
import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";

export type MessageRecoveryHandlers = {
  /** The host rejects edit and rewind until the session is idle. */
  held: () => boolean;
  onEdit: (messageId: string, text: string) => void;
  onRewind: (messageId: string, text: string) => void;
  onCopy: (text: string) => void;
};

export function UserBubble(props: {
  content: string;
  pending?: PendingSend;
  contentParts?: readonly MessageContentPart[] | null;
  /** Host-stamped replacement spans for this message copy. */
  redaction?: HostSecretRedactionMeta | null;
  artifactIds?: readonly string[];
  sessionId?: string | null;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  /** Session's active root; bare chip paths resolve against it when the part pins none. */
  activeRootId?: string;
  entryKey?: string;
  rowId?: string;
  client?: import("../../api/client.ts").LycaonClient | null;
  recovery?: MessageRecoveryHandlers;
  /** Reserves the action strip while pending so confirmation does not shift the row. */
  reserveActions?: boolean;
  /** Host timestamp, or the local seat instant while pending. */
  ts?: string;
  viewContext?: ViewContext | null;
}) {
  const chatDestination = useChatDestinationScope();
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.entryKey,
    skipFade: true,
  }));
  const projection = createMemo(() =>
    projectUserMessageParts(props.content, props.contentParts),
  );
  /** Whole-message offsets apply only to an unprojected message. */
  const paintableContentRedaction = createMemo(() =>
    (props.contentParts?.length ?? 0) === 0 ? props.redaction : undefined,
  );
  const presentIds = () => props.artifactIds ?? [];
  const presentRowKey = () => props.rowId?.trim() || props.entryKey || "user";
  const anchorId = () => props.rowId?.trim() || props.entryKey || "";
  const actions = createMemo(() => {
    const recovery = props.recovery;
    if (!recovery) return [];
    const id = anchorId() || "actions";
    return buildUserMessageActions({
      onCopy: () => recovery.onCopy(projection().copyText),
      onEdit: () => recovery.onEdit(id, projection().copyText),
      onRewind: () => recovery.onRewind(id, projection().copyText),
      recoveryHeld: recovery.held(),
    });
  });
  const hasActionSlot = () => Boolean(props.recovery) || props.reserveActions === true;
  const showFooter = () =>
    hasActionSlot() || Boolean(props.ts && messageTimesPref() === "always");
  const chipNotices = useNoticesOptional();
  const [pathMenu, setPathMenu] = createSignal<ReturnType<typeof markdownProjectPathLinkContextMenu>>(null);
  const openChipPath = (chip: TranscriptAttachmentChip) => {
    const projectId = props.projectId?.trim();
    const target = transcriptChipTarget(chip);
    if (!projectId || !target) return;
    void openSourceLocation({
      intent: "permanent",
      projectId,
      rootId: target.rootId,
      activeRootId: props.activeRootId,
      path: target.path,
      entryKind: target.entryKind,
      line: target.startLine,
      endLine: target.endLine,
    }).then((result) => {
      if (result.status !== "noop") return;
      chipNotices?.publish({
        severity: "warning",
        title: "Could not open that path",
        message: `${target.path} is not inside an attached folder of this project.`,
      });
    });
  };
  return (
    <div class="user-turn">
      <article
        ref={bindTranscriptEntry}
        class="bubble bubble--user"
        aria-label="You"
        classList={{ "bubble--reserved": props.pending?.kind === "queue_send" }}
        data-testid={props.pending ? "pending-send-bubble" : "transcript-article-user"}
        data-operation-id={props.pending?.operationId}
        data-pending-kind={props.pending?.kind}
      >
        <Show when={presentIds().length > 0}>
          <VisualPresentBlock
            artifactIds={presentIds()}
            sessionId={props.sessionId}
            projectId={props.projectId}
            client={props.client}
            rowKey={presentRowKey()}
          />
        </Show>
        <Show when={(props.pending?.attachmentLabels?.length ?? 0) > 0}>
          <Scrollport
            class="den-composer-chip-rail den-user-attach-chips"
            contentAs="ul"
            contentClass="den-composer-chip-list"
            content={{ "data-testid": "pending-send-attachment-chips" }}
          >
            <For each={props.pending?.attachmentLabels}>
              {(label) => (
                <li>
                  <span
                    class="den-composer-chip"
                    data-tip={label}
                    data-tip-when-clipped=".den-composer-chip-label"
                  >
                    <span class="den-composer-chip-label">{label}</span>
                  </span>
                </li>
              )}
            </For>
          </Scrollport>
        </Show>
        <Show when={projection().chips.length > 0}>
          <Scrollport
            class="den-composer-chip-rail den-user-attach-chips"
            contentAs="ul"
            contentClass="den-composer-chip-list"
            content={{ "data-testid": "transcript-attachment-chips" }}
          >
            <For each={projection().chips}>
              {(chip) => (
                <li>
                  <Show
                    when={transcriptChipTarget(chip)}
                    fallback={
                      <span
                        class="den-status-mark den-user-attach-chip"
                        data-testid="transcript-attachment-chip"
                        data-kind={chip.kind}
                        data-tip={chip.detail ? `${chip.label} · ${chip.detail}` : chip.label}
                        data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
                      >
                        <span class="den-composer-chip-glyph" aria-hidden="true">
                          {attachmentKindGlyph(chip.kind)}
                        </span>
                        <span class="den-composer-chip-label">{chip.label}</span>
                        <Show when={chip.detail}>
                          <span class="den-composer-chip-detail">{chip.detail}</span>
                        </Show>
                      </span>
                    }
                  >
                    <button
                      type="button"
                      class="den-inline-control den-user-attach-chip"
                      data-testid="transcript-attachment-chip"
                      data-kind={chip.kind}
                      data-tip={chip.detail ? `${chip.label} · ${chip.detail}` : chip.label}
                      data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
                      aria-label={`Open ${chip.label}`}
                      data-project-id={props.projectId}
                      data-den-source-path={transcriptChipTarget(chip)?.path}
                      data-den-project-root-id={transcriptChipTarget(chip)?.rootId}
                      data-den-project-path-kind={transcriptChipTarget(chip)?.entryKind}
                      data-den-source-line={transcriptChipTarget(chip)?.startLine}
                      data-den-source-end-line={transcriptChipTarget(chip)?.endLine}
                      onClick={() => openChipPath(chip)}
                      onContextMenu={(event) => {
                        const next = markdownProjectPathLinkContextMenu(event, {
                          projectId: props.projectId, rootRefs: props.rootRefs,
                          activeRootId: props.activeRootId,
                          chatDestination: chatDestination(),
                        });
                        if (next) setPathMenu(next);
                      }}
                    >
                      <span class="den-composer-chip-glyph" aria-hidden="true">
                        {attachmentKindGlyph(chip.kind)}
                      </span>
                      <span class="den-composer-chip-label">{chip.label}</span>
                      <Show when={chip.detail}>
                        <span class="den-composer-chip-detail">{chip.detail}</span>
                      </Show>
                    </button>
                  </Show>
                </li>
              )}
            </For>
          </Scrollport>
        </Show>
        <Show when={projection().prose.trim()}>
          <Show when={paintableContentRedaction()} fallback={projection().prose} keyed>
            {(redaction) => (
              <RedactedText
                message={{ host_secret_redaction: redaction }}
                field={REDACTION_FIELD_CONTENT}
                text={projection().prose}
              />
            )}
          </Show>
        </Show>
        <Show when={pathMenu()} keyed>{(menu) => <ContextMenu anchor={menu.anchor} items={menu.items} onDismiss={() => setPathMenu(null)} />}</Show>
      </article>
      <Show when={props.viewContext} keyed>
        {(view) => <ViewContextLine view={view} projectId={props.projectId} />}
      </Show>
      <Show when={showFooter()}>
        <div
          class="den-user-msg-footer"
          classList={{ "den-user-msg-footer--actions": hasActionSlot() }}
        >
          <Show when={actions().length > 0}>
            <MessageActions
              align="end"
              ts={messageTimesPref() === "hover" ? props.ts : undefined}
              actions={actions()}
            />
          </Show>
          <Show when={props.ts && messageTimesPref() === "always" ? props.ts : undefined} keyed>
            {(ts) => <MessageTime ts={ts} placement="below" />}
          </Show>
        </div>
      </Show>
    </div>
  );
}
