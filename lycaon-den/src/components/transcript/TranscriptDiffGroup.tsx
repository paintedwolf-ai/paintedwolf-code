import { createMemo, createSignal, Show, type JSX } from "solid-js";
import {
  fileEditLineStat,
  fileEditFoldFingerprint,
  type FileEditFold,
} from "../../chat/file-edit/file-edit-fold.ts";
import {
  createRowAccordion,
  RowAccordionProvider,
} from "../../chat/transcript/presentation/row-accordion.ts";
import { useTranscriptDisclosure } from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { useTranscriptDisclosureStore } from "../../chat/transcript/presentation/disclosure-state.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import { heightToggleDirectionFor } from "../../ui/height-toggle-motion.ts";
import { KeyedIndex } from "../keyed-index.tsx";
import { FileEditDiff } from "../FileEditDiff.tsx";

type Props = {
  folds: readonly FileEditFold[];
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  /** Worker overlay; null selects the project tree. */
  jobId?: string | null;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  entryKey: string;
};

function LineStat(props: { added: number; removed: number }) {
  return (
    <span
      class="den-diff-group-stat"
      aria-label={`${props.added} added, ${props.removed} removed`}
    >
      <span class="den-file-edit-diff-stat-add">+{props.added}</span>
      <span class="den-file-edit-diff-stat-del">−{props.removed}</span>
    </span>
  );
}

export function TranscriptDiffGroup(props: Props) {
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.entryKey,
  }));
  const {
    key: disclosureKey,
    open,
    onToggle,
    onSummaryClick,
    revealTemporarily,
  } = useTranscriptDisclosure(() => transcriptDisclosureKey.diffGroup(props.entryKey));
  // Each file's choice lives in the transcript's open state, so it survives remounts.
  const localAccordion = createRowAccordion();
  const disclosures = useTranscriptDisclosureStore() ?? useTranscriptViewport()?.disclosures;
  const accordion = {
    ...localAccordion,
    heldOpen: (foldKey: string) =>
      disclosures
        ? disclosures.isOpen(transcriptDisclosureKey.diffFile(foldKey))
        : localAccordion.heldOpen?.(foldKey),
    recordOpen: (foldKey: string, value: boolean) => {
      localAccordion.recordOpen?.(foldKey, value);
      const key = transcriptDisclosureKey.diffFile(foldKey);
      if (disclosures && disclosures.isOpen(key) !== value) disclosures.setUserOpen(key, value);
    },
  };
  const [hostEl, setHostEl] = createSignal<HTMLElement | null>(null);

  bindFindRevealHost({
    id: `diff-group-reveal:${props.entryKey}`,
    hostEl,
    isCollapsed: () => !open(),
    revealForFind: revealTemporarily,
  });

  const [netStats, setNetStats] = createSignal(new Map<string, { added: number; removed: number }>());
  const summary = createMemo(() => {
    const stats = props.folds.map(fold => fileEditLineStat(fold.net) ?? netStats().get(fileEditFoldFingerprint(fold)));
    let total: { added: number; removed: number } | null = { added: 0, removed: 0 };
    for (const stat of stats) {
      if (!stat) { total = null; break; }
      total.added += stat.added;
      total.removed += stat.removed;
    }
    return {
      files: props.folds.length,
      writes: props.folds.reduce((sum, fold) => sum + fold.steps.length, 0),
      stat: total,
    };
  });
  const fileLabel = () =>
    summary().files === 1 ? "1 file changed" : `${summary().files} files changed`;
  const writeLabel = () =>
    summary().writes === 1 ? "1 write" : `${summary().writes} writes`;
  const handleSummaryClick: JSX.EventHandler<HTMLElement, MouseEvent> = (
    event,
  ) => {
    const summary = event.currentTarget;
    const details = summary.parentElement;
    const expanding = details instanceof HTMLDetailsElement
      ? heightToggleDirectionFor(details, details.open) === "open"
      : !open();
    const fold = props.folds[0];
    if (expanding && props.folds.length === 1 && fold) {
      accordion.open(fold.key);
    }
    onSummaryClick(event);
  };

  return (
    <details
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class="den-diff-group den-transcript-disclosure-card"
      data-testid="diff-group"
      data-files={summary().files}
      data-layout={props.layout}
      data-disclosure-key={disclosureKey}
      open={open()}
      onToggle={onToggle}
    >
      <summary
        onClick={handleSummaryClick}
        aria-label={`${fileLabel()}, ${summary().stat?.added ?? "pending"} added, ${
          summary().stat?.removed ?? "pending"
        } removed, ${writeLabel()}`}
      >
        <span class="den-diff-group-summary">
          <span class="den-diff-group-files">{fileLabel()}</span>
          <Show keyed when={summary().stat}>{stat => <LineStat added={stat.added} removed={stat.removed} />}</Show>
          <span class="den-diff-group-writes">{writeLabel()}</span>
          <span class="den-tool-chicklet-caret" aria-hidden="true" />
        </span>
      </summary>
      <div class="den-diff-group-list">
        <RowAccordionProvider value={accordion}>
          <KeyedIndex each={props.folds} keyOf={(fold) => fold.key}>
            {(fold) => (
              <FileEditDiff
                chrome="row"
                fold={fold()}
                disclosureKey={transcriptDisclosureKey.diffFile(fold().key)}
                onNetStat={stat => setNetStats(previous => new Map([...previous].filter(([key]) => props.folds.some(fold => fileEditFoldFingerprint(fold) === key))).set(fileEditFoldFingerprint(fold()), stat))}
                projectId={props.projectId}
                sessionId={props.sessionId}
                jobId={props.jobId}
                rootRefs={props.rootRefs}
              />
            )}
          </KeyedIndex>
        </RowAccordionProvider>
      </div>
    </details>
  );
}
