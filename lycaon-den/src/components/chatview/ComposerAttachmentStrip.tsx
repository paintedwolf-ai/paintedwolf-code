import { For, Show } from "solid-js";
import {
  ATTACHMENT_PROVIDER_SEND_HINT,
  attachmentChipDetail,
  attachmentChipLabel,
  attachmentKindGlyph,
  formatAttachmentBytes,
  type ComposerPendingAttachment,
} from "../../chat/composer/composer-attachments.ts";
import type { createPastedTextAttachmentController } from "../../chat/composer/pasted-text-attachment-controller.ts";
import type { WorkflowSlashSuggestion } from "../../chat/composer/composer-workflow-triggers.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { chipTitle } from "./useComposerAttachments.ts";

type PastedTextUploadEntry = ReturnType<
  ReturnType<typeof createPastedTextAttachmentController>["visibleUploads"]
>[number];

export type ComposerAttachmentStripProps = {
  attachments: readonly ComposerPendingAttachment[];
  visiblePastedTextUploads: readonly PastedTextUploadEntry[];
  hasByteAttachments: boolean;
  onClearAttachments: () => void;
  onRetryPastedUpload: (upload: PastedTextUploadEntry) => void;
  onRemovePastedUpload: (id: string) => void;
  onRemoveAttachment: (id: string) => void;
  showSlashSuggestions: boolean;
  slashListboxId: string;
  slashSuggestions: readonly WorkflowSlashSuggestion[];
  activeSlashSuggestion: number;
  anchor: () => HTMLElement | undefined;
  onDismissSlash: () => void;
  onSelectSlashIndex: (index: number) => void;
  onApplySlashSuggestion: (trigger: string) => void;
};

export function ComposerAttachmentStrip(props: ComposerAttachmentStripProps) {
  return (
    <>
      <Show when={props.attachments.length > 0 || props.visiblePastedTextUploads.length > 0}>
        <div class="den-composer-attach-note">
          <span data-testid="composer-attach-count">
            {props.visiblePastedTextUploads.some((upload) => upload.status === "uploading")
              ? `${props.visiblePastedTextUploads.filter((upload) => upload.status === "uploading").length} attaching`
              : props.visiblePastedTextUploads.some((upload) => upload.status === "failed")
                ? `${props.visiblePastedTextUploads.filter((upload) => upload.status === "failed").length} needs attention`
                : props.attachments.length === 1
                  ? "1 attached"
                  : `${props.attachments.length} attached`}
          </span>
          <Show when={props.hasByteAttachments || props.visiblePastedTextUploads.length > 0}>
            <span class="den-composer-attach-note-hint" data-testid="composer-provider-send-hint">
              {ATTACHMENT_PROVIDER_SEND_HINT}
            </span>
          </Show>
          <button
            type="button"
            class="den-composer-attach-clear"
            data-testid="composer-attach-clear"
            onClick={props.onClearAttachments}
          >
            Clear all
          </button>
        </div>
        <Scrollport
          class="den-composer-chip-rail"
          contentAs="ul"
          contentClass="den-composer-chip-list"
          content={{ "data-testid": "composer-attachment-chips" }}
        >
          <For each={props.visiblePastedTextUploads}>
            {(upload) => (
              <li
                class="den-composer-chip"
                classList={{ "den-composer-chip--reject": upload.status === "failed" }}
                data-testid="composer-pasted-text-upload"
                data-kind="text"
                aria-busy={upload.status === "uploading" ? "true" : undefined}
                data-tip={`${upload.file.name} · ${formatAttachmentBytes(upload.file.size)}`}
                data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
              >
                <span class="den-composer-chip-glyph" aria-hidden="true">
                  ≡
                </span>
                <span class="den-composer-chip-label">{upload.file.name}</span>
                <span class="den-composer-chip-detail">
                  {upload.status === "uploading"
                    ? `${formatAttachmentBytes(upload.file.size)} · Attaching…`
                    : upload.error}
                </span>
                <Show when={upload.status === "failed"}>
                  <button
                    type="button"
                    class="den-composer-chip-remove"
                    data-testid="composer-pasted-text-retry"
                    aria-label="Retry pasted text attachment"
                    onClick={() => props.onRetryPastedUpload(upload)}
                  >
                    ↻
                  </button>
                </Show>
                <button
                  type="button"
                  class="den-composer-chip-remove"
                  data-testid="composer-pasted-text-remove"
                  aria-label="Remove pasted text attachment"
                  onClick={() => props.onRemovePastedUpload(upload.id)}
                >
                  ×
                </button>
              </li>
            )}
          </For>
          <For each={props.attachments}>
            {(att) => (
              <li
                class="den-composer-chip"
                classList={{ "den-composer-chip--reject": att.kind === "reject" }}
                data-testid="composer-attachment-chip"
                data-kind={att.kind}
                data-reject-code={att.kind === "reject" ? (att.rejectCode ?? "") : ""}
                data-tip={chipTitle(att)}
                data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
              >
                <Show
                  when={
                    "previewUrl" in att && att.previewUrl ? att.previewUrl : undefined
                  }
                  keyed
                  fallback={
                    <span class="den-composer-chip-glyph" aria-hidden="true">
                      {attachmentKindGlyph(att.kind)}
                    </span>
                  }
                >
                  {(url) => (
                    <img
                      class="den-composer-chip-thumb"
                      src={url}
                      alt={
                        att.kind === "image"
                          ? `${att.name} (${formatAttachmentBytes(att.byteLength)})`
                          : att.name
                      }
                    />
                  )}
                </Show>
                <span class="den-composer-chip-label">{attachmentChipLabel(att)}</span>
                <Show when={attachmentChipDetail(att)}>
                  <span class="den-composer-chip-detail" data-testid="composer-attach-preview">
                    {attachmentChipDetail(att)}
                  </span>
                </Show>
                <button
                  type="button"
                  class="den-composer-chip-remove"
                  data-testid="composer-image-remove"
                  aria-label={`Remove ${attachmentChipLabel(att)}`}
                  onClick={() => props.onRemoveAttachment(att.id)}
                >
                  ×
                </button>
              </li>
            )}
          </For>
        </Scrollport>
      </Show>
      <Show when={props.showSlashSuggestions}>
        <AnchoredSurface
          id={props.slashListboxId}
          class="den-composer-slash-suggestions"
          testId="composer-slash-suggestions"
          role="listbox"
          ariaLabel="Workflow slash commands"
          anchor={props.anchor}
          preferredSide="top"
          width="anchor"
          gap={6}
          onDismiss={props.onDismissSlash}
        >
          <For each={props.slashSuggestions}>
            {(row, index) => (
              <button
                id={`${props.slashListboxId}-option-${index()}`}
                type="button"
                role="option"
                tabindex={-1}
                aria-selected={index() === props.activeSlashSuggestion}
                class="den-composer-slash-suggestion"
                classList={{
                  "den-composer-slash-suggestion--active":
                    index() === props.activeSlashSuggestion,
                }}
                data-testid={`composer-slash-${row.trigger.slice(1)}`}
                onMouseEnter={() => props.onSelectSlashIndex(index())}
                onClick={() => props.onApplySlashSuggestion(row.trigger)}
              >
                <span class="den-composer-slash-suggestion-trigger">{row.trigger}</span>
                <span class="den-composer-slash-suggestion-label">{row.label}</span>
              </button>
            )}
          </For>
        </AnchoredSurface>
      </Show>
    </>
  );
}
