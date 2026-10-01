import { For, Show } from "solid-js";
import type { SourceWalkEffect } from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { isCallerPerson } from "../../platform/connection/host-identity.ts";
import {
  contributorLabel,
  contributorsLabel,
  effectContributors,
  opGlyph,
  type ReviewContributor,
  type ReviewListRow,
} from "./review-model.ts";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";
import type { ChatAttachmentRef } from "../../chat/composer/add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";

type Props = {
  row: ReviewListRow;
  /** Some visible row has line counts, so every row holds the column. */
  reserveStat: boolean;
  expanded: boolean;
  stepsOpen: boolean;
  onOpen: () => void;
  onRevert?: () => void;
  onToggleSteps: () => void;
  onOpenStep: (step: SourceWalkEffect) => void;
  onRowMenu: (anchor: { clientX: number; clientY: number }) => void;
  chatRef?: ChatAttachmentRef | null;
  /** Already seen at the reader's latest look. */
  seen?: boolean;
  /** Just moved from New into Seen. */
  arrived?: boolean;
  onMarkUnseen?: () => void;
  onViewReviewed?: () => void;
  loadingReviewed?: boolean;
};

function contributorTitle(contributor: ReviewContributor): string {
  switch (contributor.kind) {
    case "agent":
      return `From ${contributorLabel(contributor)}`;
    case "user":
      return isCallerPerson(contributor.personId)
        ? "Your contribution"
        : "Another person's contribution";
    case "command":
      return "Changed while a command ran";
    case "external":
      return "Changed outside the app";
  }
}

function stepActor(step: SourceWalkEffect): string {
  return contributorsLabel(effectContributors(step));
}

function stepOperation(step: SourceWalkEffect): string {
  if (step.op === "delete") return "Deleted";
  if (step.op === "create") return "Created";
  return "Modified";
}

function deterministicDetail(row: ReviewListRow): string {
  const operation =
    row.op === "write" ? "Modified" : row.op === "delete" ? "Deleted" : "Created";
  const stats = [
    row.added ? `+${row.added}` : "",
    row.removed ? `−${row.removed}` : "",
  ].filter(Boolean);
  const revisions = row.steps.length > 1 ? `${row.steps.length} revisions` : "";
  // Changed rows show a HEAD badge only when their bytes match.
  const committed = row.headMatch === "same" ? "matches HEAD" : "";
  const git = row.commit?.status;
  const status = git === "??" ? "Untracked"
    : git && (git.includes("U") || git === "AA" || git === "DD") ? "Merge conflict"
    : git ? [git[0] !== "." ? "Staged" : "", git[1] !== "." ? "Unstaged" : ""].filter(Boolean).join(" + ")
    : "";
  return [operation, status, stats.join(" "), revisions, committed]
    .filter(Boolean)
    .join(" · ");
}

export function ReviewFileRow(props: Props) {
  return (
    <div
      class="den-review-file"
      classList={{
        "den-review-file--seen": props.seen === true,
        "den-review-file--arrived": props.arrived === true,
      }}
    >
      <div class="den-review-file__line">
        <button
          type="button"
          class="den-review-row"
          classList={{
            "den-review-row--open": props.expanded,
            "den-review-row--live": props.row.liveVerb !== null,
          }}
          data-testid={
            props.row.liveVerb && props.row.steps.length === 0
              ? "changes-row-live"
              : "changes-row-file"
          }
          data-files-ctx="changed-file"
          data-root-id={props.row.rootId}
          data-path={props.row.path}
          draggable={props.chatRef ? true : undefined}
          onDragStart={(event) => startChatAttachmentDrag(event, props.chatRef)}
          data-tip={props.row.path}
          onClick={() => props.onOpen()}
          onContextMenu={(event) => {
            event.preventDefault();
            props.onRowMenu({
              clientX: event.clientX,
              clientY: event.clientY,
            });
          }}
        >
          <span class="den-review-row__op" aria-hidden="true">
            <Show
              when={props.row.liveVerb}
              fallback={opGlyph(props.row.op)}
            >
              <span class="den-review-row__pulse" />
            </Show>
          </span>
          <span class="den-review-row__identity">
            <span
              class="den-review-row__base"
              data-file-change={props.row.tip.state === "absent" ? "deleted" : wholeFileOpChange(props.row.op)}
            >
              {props.row.basename}
            </span>
            <Show when={props.row.tip.state === "absent"}>
              <span class="den-review-row__dir">Deleted</span>
            </Show>
            <Show when={props.row.dirname}>
              <span class="den-review-row__dir">{props.row.dirname}</span>
            </Show>
          </span>
          <Show when={props.row.contributors.length > 0}>
            <span
              class="den-review-row__contributors"
              aria-label={props.row.attribution}
            >
              <For each={props.row.contributors}>
                {(origin) => (
                  <span
                    class={`den-review-contributor den-review-contributor--${origin.kind}`}
                    data-tip={props.row.commit ? `Recorded history: ${contributorTitle(origin)}` : contributorTitle(origin)}
                  />
                )}
              </For>
            </span>
          </Show>
          <Show
            when={props.row.liveVerb}
            fallback={
              <Show when={props.reserveStat}>
                <span class="den-review-row__stat">
                  <Show when={props.row.added}>
                    <span class="den-review-row__added">+{props.row.added}</span>
                  </Show>
                  <Show when={props.row.removed}>
                    <span class="den-review-row__removed">
                      −{props.row.removed}
                    </span>
                  </Show>
                </span>
              </Show>
            }
          >
            {(verb) => <span class="den-review-row__verb">{verb()}</span>}
          </Show>
        </button>

        <Show when={props.onMarkUnseen}>
          <button
            type="button"
            class="den-review-unseen den-icon-target"
            data-testid="changes-row-mark-unseen"
            aria-label={`Mark ${props.row.basename} unseen`}
            data-tip="Mark unseen"
            onClick={(event) => {
              event.stopPropagation();
              props.onMarkUnseen?.();
            }}
          >
            <ThemeIcon slot="scope" size={14} />
          </button>
        </Show>

        <Show when={props.onRevert && !props.row.liveVerb && (!props.row.commit || (props.row.headMatch !== "same" && ["available", "absent"].includes(props.row.commit.availability)))}>
          <button
            type="button"
            class="den-review-revert den-icon-target"
            classList={{ "den-review-revert--ready": props.row.op === "delete" }}
            data-testid="changes-row-revert"
            aria-label={props.row.commit ? `Restore working file ${props.row.basename} to last commit` : `Restore ${props.row.basename} to comparison baseline`}
            data-tip={props.row.commit ? "Restore working file to last commit; staged changes stay in the index" : `Restore ${props.row.basename} to comparison baseline`}
            onClick={(event) => {
              event.stopPropagation();
              props.onRevert?.();
            }}
          >
            <ThemeIcon slot="rewind" size={14} />
          </button>
        </Show>
      </div>

      <Show when={props.expanded && !props.row.liveVerb}>
        <div class="den-review-detail" data-testid="changes-row-detail">
          <p class="den-review-detail__summary">{deterministicDetail(props.row)}</p>
          <Show when={props.onViewReviewed}>
            <div class="my-1 flex">
              <DenButton variant="secondary" compact
                data-testid="changes-row-view-reviewed" disabled={props.loadingReviewed}
                onClick={() => props.onViewReviewed?.()}>
                {props.loadingReviewed ? "Loading reviewed changes…" : "View reviewed changes"}
              </DenButton>
            </div>
          </Show>
          <Show when={props.row.commit && !["available", "absent"].includes(props.row.commit?.availability ?? "unavailable")}>
            <p class="den-review-detail__summary">Content preview is unavailable for this path. Git still reports it as changed.</p>
          </Show>
          <Show when={props.row.commit?.history_truncated}>
            <p class="den-review-detail__summary">Showing recent recorded history. Open the file’s version picker for earlier versions.</p>
          </Show>
          <div class="den-review-detail__meta">
            <div class="flex min-w-0 flex-1 flex-wrap items-center gap-1">
              <For each={props.row.contributors}>{(author, index) => <>
                <Show when={index() > 0}><span>+</span></Show>
                <span class="min-w-0 truncate" data-tip={contributorLabel(author)}>{contributorLabel(author)}</span>
              </>}</For>
              <Show when={props.row.lastTs}>
                <span class="shrink-0 whitespace-nowrap">
                  {relativeTimeLabel(props.row.lastTs)}
                </span>
              </Show>
            </div>
            <Show when={props.row.steps.length > 1}>
              <DenButton
                variant="ghost"
                compact
                class="shrink-0"
                data-testid="changes-row-steps-toggle"
                aria-expanded={props.stepsOpen}
                onClick={() => props.onToggleSteps()}
              >
                {props.stepsOpen
                  ? "Hide steps"
                  : `${props.row.steps.length} steps`}
              </DenButton>
            </Show>
          </div>
          <Show when={props.stepsOpen && props.row.steps.length > 1}>
            <ul class="den-review-steps">
              <For each={props.row.steps}>
                {(step) => (
                  <li>
                    <button
                      type="button"
                      class="den-review-step"
                      data-testid="changes-step"
                      onClick={() => props.onOpenStep(step)}
                    >
                      {stepOperation(step)}
                      {" · "}
                      {relativeTimeLabel(step.observed_at)}
                      {" · "}
                      {stepActor(step)}
                      {step.capture_quality !== "exact"
                        ? ` · ${step.capture_quality}`
                        : ""}
                      {step.turn ? ` · turn ${step.turn}` : ""}
                    </button>
                  </li>
                )}
              </For>
            </ul>
          </Show>
        </div>
      </Show>
    </div>
  );
}
