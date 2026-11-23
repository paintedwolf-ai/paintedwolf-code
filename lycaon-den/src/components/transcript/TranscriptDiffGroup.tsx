import { createMemo, createSignal, Show, type JSX } from "solid-js";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import {
  fileEditLineStat,
  fileEditFoldFingerprint,
  type FileEditFold,
} from "../../chat/file-edit/file-edit-fold.ts";
import { createFoldNetStats } from "../../chat/file-edit/file-edit-net-stats.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { useTranscriptDisclosureStore } from "../../chat/transcript/presentation/disclosure-state.tsx";
import { createRowAccordion, RowAccordionProvider } from "../../chat/transcript/presentation/row-accordion.ts";
import {
  TranscriptDisclosurePanel,
  TranscriptDisclosureShell,
  useTranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";
import { transcriptDisclosureKey, type TranscriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { revealChicklet } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { heightToggleDirectionFor } from "../../ui/height-toggle-motion.ts";
import { FileEditDiff } from "../FileEditDiff.tsx";
import { VirtualCardList } from "../primitives/VirtualCardList.tsx";
import { SOURCE_COMPARISON_FAILURE } from "../source/reader/SourceReader.tsx";
import { TranscriptLinkSummary } from "./TranscriptLinkSummary.tsx";

type Props = {
  folds: readonly FileEditFold[];
  /** Changes made past the results' bounds; Review lists them. */
  omitted?: number;
  layout: TranscriptLayout;
  sessionId?: string | null;
  projectId?: string;
  /** Worker overlay; null selects the project tree. */
  jobId?: string | null;
  rootRefs?: readonly ResolveProjectRoot[];
  entryKey: string;
  chrome?: "card" | "section";
  disclosureKey?: TranscriptDisclosureKey;
  onShowActivity?: (toolCallId: string, path: string) => void;
};

/** Names the files shown and, past a result's bound, the changes Review holds. */
export function diffGroupFileLabel(
  files: number,
  omitted: number,
  verb: "changed" | "edited" = "changed",
): string {
  const more = omitted === 1 ? "1 more change in Review" : `${omitted} more changes in Review`;
  if (files === 0) return omitted === 1 ? "1 change in Review" : `${omitted} changes in Review`;
  const shown = files === 1 ? `1 file ${verb}` : `${files} files ${verb}`;
  return omitted > 0 ? `${shown}, ${more}` : shown;
}

export function diffGroupFilenameHint(
  folds: readonly FileEditFold[],
  rootRefs?: readonly ResolveProjectRoot[],
): { display: string; full: string } | null {
  if (folds.length === 0) return null;
  const basenames = folds.map((fold) => fold.path.split("/").filter(Boolean).pop() ?? fold.path);
  const counts = new Map<string, number>();
  for (const base of basenames) counts.set(base, (counts.get(base) ?? 0) + 1);
  const rootNameById = new Map<string, string>();
  for (const ref of rootRefs ?? []) {
    const id = ref.id?.trim();
    const label = ref.path?.split("/").filter(Boolean).pop() || id;
    if (id && label) rootNameById.set(id, label);
  }
  const names = folds.map((fold, index) => {
    const base = basenames[index]!;
    if ((counts.get(base) ?? 0) <= 1) return base;
    const rootId = fold.net.root_id?.trim();
    const rootLabel = (rootId && (rootNameById.get(rootId) ?? rootId)) || fold.path.split("/").slice(-2, -1)[0];
    return rootLabel ? `${rootLabel}/${base}` : fold.path;
  });
  const full = names.join(" · ");
  const shown = names.slice(0, 2);
  const extra = names.length - shown.length;
  const display = extra > 0 ? `${shown.join(" · ")} · +${extra}` : shown.join(" · ");
  return { display, full };
}

function LineStat(props: { added: number; removed: number }) {
  return (
    <span
      class="den-diff-group-stat"
      data-testid="diff-group-net"
      aria-label={`${props.added} added, ${props.removed} removed`}
    >
      <span class="den-file-edit-diff-stat-add">+{props.added}</span>
      <span class="den-file-edit-diff-stat-del">−{props.removed}</span>
    </span>
  );
}

export function TranscriptDiffGroup(props: Props) {
  const asSection = () => props.chrome === "section";
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.entryKey,
    skipFade: asSection(),
  }));
  const disclosure = useTranscriptDisclosure(() =>
    props.disclosureKey ?? transcriptDisclosureKey.diffGroup(props.entryKey),
  );
  const { open, onToggleClick, revealTemporarily } = disclosure;
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
    collapsedCorpus: () => props.folds.map((fold) => fold.path).join("\n"),
    collapsedCorpusAnchor: hostEl,
  });

  const measured = createFoldNetStats(() => props.folds, () => props.projectId, () => props.sessionId ?? undefined);
  observeSurfaceFailure(SOURCE_COMPARISON_FAILURE, measured.error, () => props.projectId);
  const [netStats, setNetStats] = createSignal(new Map<string, { added: number; removed: number }>());
  const netStatOf = (fold: FileEditFold) =>
    fileEditLineStat(fold.net) ?? measured.stats().get(fileEditFoldFingerprint(fold)) ?? netStats().get(fileEditFoldFingerprint(fold));
  const summary = createMemo(() => {
    const stats = props.folds.map(netStatOf);
    let total: { added: number; removed: number } | null = { added: 0, removed: 0 };
    for (const stat of stats) {
      if (!stat) {
        total = null;
        break;
      }
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
    diffGroupFileLabel(summary().files, props.omitted ?? 0, asSection() ? "edited" : "changed");
  const writeLabel = () => (summary().writes === 1 ? "1 write" : `${summary().writes} writes`);
  const filenameHint = createMemo(() =>
    asSection() ? diffGroupFilenameHint(props.folds, props.rootRefs) : null,
  );
  const pinnedFoldKeys = createMemo(() =>
    props.folds.filter((fold) => accordion.heldOpen(fold.key)).map((fold) => fold.key),
  );
  const showActivity = (target: { toolCallId: string; path: string }) => {
    if (props.onShowActivity) {
      props.onShowActivity(target.toolCallId, target.path);
      return;
    }
    const sid = props.sessionId?.trim();
    if (!sid || props.layout !== "chat") return;
    void revealChicklet(
      () => hostEl()?.closest<HTMLElement>(".den-chat-stream") ?? document.querySelector<HTMLElement>(".den-chat-stream"),
      {
        sessionId: sid,
        anchor: { chicklet: "tool", anchorId: target.toolCallId, path: target.path, persistExpansion: true },
      },
    );
  };
  const canShowActivity = () =>
    Boolean(props.onShowActivity || (props.layout === "chat" && props.sessionId?.trim()));

  const handleSummaryClick: JSX.EventHandler<HTMLButtonElement, MouseEvent> = (event) => {
    const host = hostEl();
    const expanding = host ? heightToggleDirectionFor(host, open()) === "open" : !open();
    const fold = props.folds[0];
    if (expanding && props.folds.length === 1 && fold) {
      accordion.open(fold.key);
    }
    onToggleClick(event);
  };

  return (
    <TranscriptDisclosureShell
      disclosure={disclosure}
      ref={(el) => {
        bindTranscriptEntry(el);
        setHostEl(el);
      }}
      class={
        asSection()
          ? "den-diff-group den-diff-group--section den-transcript-disclosure-card"
          : "den-diff-group den-transcript-card den-transcript-disclosure-card den-transcript-disclosure-link"
      }
      data-testid="diff-group"
      data-chrome={asSection() ? "section" : "card"}
      data-files={summary().files}
      data-layout={props.layout}
    >
      <TranscriptLinkSummary
        disclosure={disclosure}
        label={fileLabel()}
        labelTestId="diff-group-files"
        onClick={handleSummaryClick}
        aria-label={`${fileLabel()}, ${summary().stat?.added ?? "pending"} added, ${
          summary().stat?.removed ?? "pending"
        } removed, ${writeLabel()}`}
      >
        <Show keyed when={summary().stat}>
          {(stat) => <LineStat added={stat.added} removed={stat.removed} />}
        </Show>
        <span class="den-diff-group-writes" data-testid="diff-group-writes">{writeLabel()}</span>
        <Show when={filenameHint()} keyed>
          {(hint) => (
            <span
              class="den-diff-group-hint"
              data-testid="diff-group-hint"
              data-tip={hint.full}
              data-tip-when-clipped=""
            >
              {hint.display}
            </span>
          )}
        </Show>
      </TranscriptLinkSummary>
      <TranscriptDisclosurePanel disclosure={disclosure} class="den-diff-group-list">
        <RowAccordionProvider value={accordion}>
          <VirtualCardList
            threshold={30}
            scroll="ancestor"
            keepReadingPosition
            items={props.folds}
            keyOf={(fold) => fold.key}
            pinnedKeys={pinnedFoldKeys()}
            corpus={(fold) => fold.path}
            label="Edited files"
          >
            {(fold) => (
              <FileEditDiff
                chrome="row"
                fold={fold()}
                disclosureKey={transcriptDisclosureKey.diffFile(fold().key)}
                netStat={() => measured.stats().get(fileEditFoldFingerprint(fold()))}
                onNetStat={(stat) =>
                  setNetStats((previous) =>
                    new Map(
                      [...previous].filter(([key]) =>
                        props.folds.some((f) => fileEditFoldFingerprint(f) === key),
                      ),
                    ).set(fileEditFoldFingerprint(fold()), stat),
                  )
                }
                projectId={props.projectId}
                sessionId={props.sessionId}
                jobId={props.jobId}
                rootRefs={props.rootRefs}
                onShowActivity={canShowActivity() ? showActivity : undefined}
              />
            )}
          </VirtualCardList>
        </RowAccordionProvider>
      </TranscriptDisclosurePanel>
    </TranscriptDisclosureShell>
  );
}
