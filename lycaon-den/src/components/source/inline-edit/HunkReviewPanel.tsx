import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { SourceEncoding } from "../../../api/types.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { writeFileContent } from "../../../files/commands/file-mutations.ts";
import {
  applyHunkReject,
  computeDiffHunks,
  type DiffLineHunk,
} from "../../../files/review/line-diff.ts";

export type HunkReviewTarget = {
  projectId: string;
  sessionId: string;
  rootId: string;
  path: string;
  before: string;
  after: string;
  /** Hash guarding reject writes. */
  headSha256: string;
  encoding: SourceEncoding;
};

type Props = {
  client: LycaonClient | null;
  target: HunkReviewTarget;
  onClose: () => void;
  onRejected?: () => void;
};

export function HunkReviewPanel(props: Props) {
  const [note, setNote] = createSignal<string | null>(null);
  const [busy, setBusy] = createSignal(false);
  const [after, setAfter] = createSignal(props.target.after);
  const [headSha256, setHeadSha256] = createSignal(props.target.headSha256);
  const [hunks, setHunks] = createSignal(
    computeDiffHunks(props.target.before, props.target.after).filter(
      (h): h is Extract<DiffLineHunk, { kind: "change" }> => h.kind === "change",
    ),
  );

  const finishHunk = (hunk: Extract<DiffLineHunk, { kind: "change" }>) => {
    const remaining = hunks().filter((candidate) => candidate !== hunk);
    setHunks(remaining);
    if (remaining.length === 0) props.onClose();
  };

  const rejectHunk = async (hunk: Extract<DiffLineHunk, { kind: "change" }>) => {
    const t = props.target;
    const c = props.client;
    if (!c) return;
    setBusy(true);
    setNote(null);
    try {
      const next = applyHunkReject(after(), t.before, hunk);
      const res = await writeFileContent(c, t.projectId, {
        rootId: t.rootId,
        path: t.path,
        content: next,
        encoding: t.encoding,
        baseSha256: headSha256(),
      }, t.sessionId);
      if (!res.ok) {
        setNote(res.conflict ? "File changed since apply" : res.error);
        return;
      }
      const lineDelta =
        (hunk.beforeEnd - hunk.beforeStart) -
        (hunk.afterEnd - hunk.afterStart);
      setAfter(next);
      setHeadSha256(res.sha256);
      setHunks((rows) => rows
        .filter((candidate) => candidate !== hunk)
        .map((candidate) =>
          candidate.afterStart >= hunk.afterEnd
            ? {
                ...candidate,
                afterStart: candidate.afterStart + lineDelta,
                afterEnd: candidate.afterEnd + lineDelta,
              }
            : candidate
        ));
      props.onRejected?.();
      if (hunks().length === 0) props.onClose();
    } catch (err) {
      setNote(err instanceof Error ? err.message : "Reject failed");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      class="flex max-h-72 flex-col border-t border-[var(--den-border)] bg-[var(--den-surface)]"
      data-testid="hunk-review-panel"
      role="region"
      aria-label="Review applied edits"
    >
      <div class="flex items-center justify-between gap-2 px-3 py-2 text-sm">
        <p class="text-[var(--den-text)]">
          Applied — review below. Reject any hunk to undo it.
        </p>
        <DenButton
          variant="ghost"
          data-testid="hunk-review-done"
          disabled={busy()}
          onClick={() => props.onClose()}
        >
          Done
        </DenButton>
      </div>
      <Show when={note()}>
        {(n) => (
          <p class="px-3 text-xs text-[var(--den-danger)]">{n()}</p>
        )}
      </Show>
      <Scrollport class="min-h-0 flex-1" contentClass="px-2 pb-2">
        <For each={hunks()}>
          {(hunk, i) => (
            <div
              class="mb-2 rounded border border-[var(--den-border)] p-2"
              data-testid="hunk-review-row"
              data-hunk-index={i()}
            >
              <div class="mb-1 flex items-center justify-end gap-2">
                <DenButton
                  variant="ghost"
                  data-testid="hunk-review-accept"
                  disabled={busy()}
                  onClick={() => finishHunk(hunk)}
                >
                  Accept
                </DenButton>
                <DenButton
                  variant="ghost"
                  data-testid="hunk-review-reject"
                  disabled={busy()}
                  onClick={() => void rejectHunk(hunk)}
                >
                  Reject
                </DenButton>
              </div>
              <Scrollport
                axis="both"
                contentAs="pre"
                contentClass="font-mono text-[11px] leading-snug whitespace-pre-wrap text-[var(--den-text-muted)]"
              >
                {hunkPreview(props.target.before, after(), hunk)}
              </Scrollport>
            </div>
          )}
        </For>
        <Show when={hunks().length === 0}>
          <p class="px-1 text-xs text-[var(--den-text-muted)]">No hunks left.</p>
        </Show>
      </Scrollport>
    </div>
  );
}

function hunkPreview(before: string, after: string, hunk: DiffLineHunk): string {
  if (hunk.kind !== "change") return "";
  const b = before.split("\n");
  const a = after.split("\n");
  const del = b
    .slice(hunk.beforeStart, hunk.beforeEnd)
    .map((l) => `- ${l}`)
    .join("\n");
  const ins = a
    .slice(hunk.afterStart, hunk.afterEnd)
    .map((l) => `+ ${l}`)
    .join("\n");
  return [del, ins].filter(Boolean).join("\n");
}
