import { Show, batch, createEffect, createMemo, createSignal, getOwner, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { DiffRowSource } from "../../components/source/diff/diff-row-source.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { getMarkMyEdits, subscribeReviewScope } from "./review-pane.ts";
import { lensEmptyCopy } from "./review-model.ts";
import { resolvedLensView, resolvedScope, scopeSessionId, subscribeResolvedScope } from "../tree/scope-resolution.ts";
import {
  diffsAddressKey,
  diffsPageCopy,
  type DiffsAddress,
  type DiffsLensState,
  type TurnDiffsAddress,
} from "./diffs-address.ts";
import {
  DIFFS_FILE_CAP,
  diffsFileKey,
  diffsFileWriteCount,
  gitInventory,
  lensInventory,
  turnInventory,
  type DiffsFile,
  type DiffsInventory,
} from "./diffs-inventory.ts";
import { loadTurnDiffs } from "./turn-diffs.ts";
import { loadGitDiffs } from "./git-diffs.ts";
import { diffsFileNetSelector, diffsFileRowSource } from "./diffs-row-source.ts";
import { createDiffsDigests } from "./diffs-digests.ts";
import { diffsTotal } from "./diffs-document.ts";
import { createDiffsReader } from "./diffs-reader.ts";
import { DiffsPageHeading } from "./DiffsPageHeading.tsx";
import { DiffsSectionHeader } from "./DiffsSectionHeader.tsx";
import { createReaderFacts } from "../../components/source/reader/source-reader-facts.tsx";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import { diffWordWrapPref, saveDiffWordWrap } from "../../settings/appearance/display-prefs.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { bindLayoutBand, LAYOUT_BAND_SCALES } from "../../layout/layout-bands.ts";
import { observeSurfaceFailure, type SurfaceFailureCopy } from "../../notices/surface-failure.ts";
import { registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";

const DIFFS_UNAVAILABLE: SurfaceFailureCopy = {
  code: "files_diffs_unavailable",
  title: "Diffs unavailable",
  suggestedAction: "Reopen the diffs page to try again.",
};

type Props = {
  projectId: string;
  client: LycaonClient | null;
  address: DiffsAddress;
  rootRefs?: readonly ResolveProjectRoot[];
  /** Opens the walk chapter a turn page names; absent for the lens page. */
  onWalkTurn?: (address: TurnDiffsAddress) => void;
};

function writeCount(count: number): string {
  return count === 1 ? "1 write" : `${count} writes`;
}

/** Virtualized view displaying all diffs in a comparison as a single document. */
export function DiffsPage(props: Props) {
  const scope = getOwner();
  const [host, setHost] = createSignal<HTMLDivElement>();
  const [error, setError] = createSignal<string>();
  const [collapsed, setCollapsed] = createSignal(new Set<string>());
  const [allOpen, setAllOpen] = createSignal(true);
  const [revisions, setRevisions] = createSignal(new Map<string, number>());
  const [reloading, setReloading] = createSignal(new Set<string>());

  const [lensRevision, setLensRevision] = createSignal(0);
  onCleanup(subscribeResolvedScope((projectId) => {
    if (projectId === props.projectId.trim()) setLensRevision((value) => value + 1);
  }));
  onCleanup(subscribeReviewScope((projectId) => {
    if (projectId === props.projectId.trim()) setLensRevision((value) => value + 1);
  }));

  const lens = createMemo<DiffsLensState>(() => {
    void lensRevision();
    return resolvedLensView(props.projectId);
  });
  const markUserEdits = createMemo(() => {
    void lensRevision();
    return props.address.kind === "lens"
      ? resolvedScope(props.projectId).markUserEdits
      : getMarkMyEdits(props.projectId);
  });
  const copy = createMemo(() => diffsPageCopy(props.address, lens()));
  const turnQuery = createSurfaceQuery({
    name: "turn-diffs",
    revalidateOnActivation: false,
    source: () => {
      const client = props.client;
      const projectId = props.projectId.trim();
      const address = props.address;
      if (!client || !projectId || address.kind !== "turn" || address.turn < 1) return null;
      const marks = markUserEdits();
      return {
        client, projectId, address, marks,
        key: JSON.stringify([projectId, diffsAddressKey(address), marks]),
        scope: JSON.stringify([projectId, address.sessionId]),
      };
    },
    scope: (source) => source.scope,
    load: (source) => loadTurnDiffs(source.client, source.projectId, source.address, source.marks),
  });

  const gitQuery = createSurfaceQuery({
    name: "git-diffs",
    revalidateOnActivation: false,
    source: () => {
      const client = props.client;
      const projectId = props.projectId.trim();
      const address = props.address;
      if (!client || !projectId || address.kind !== "git") return null;
      const key = JSON.stringify([projectId, diffsAddressKey(address)]);
      return { client, projectId, address, key, scope: key };
    },
    scope: (source) => source.scope,
    load: (source) => loadGitDiffs(source.client, source.projectId, source.address),
  });

  const inventory = createMemo<DiffsInventory>(() => {
    switch (props.address.kind) {
      case "lens":
        void lensRevision();
        return lensInventory(props.projectId);
      case "turn":
        return turnInventory(turnQuery.value(), turnQuery.error() ?? null);
      case "git":
        return gitInventory(gitQuery.value(), gitQuery.error() ?? null);
    }
  });
  const files = () => inventory().files;
  const loading = () => {
    switch (props.address.kind) {
      case "lens": return !inventory().settled && inventory().error === null;
      case "turn": return turnQuery.loading();
      case "git": return gitQuery.loading();
    }
  };
  const unread = () => inventory().error !== null || !props.client;
  // The lens page's failures are reported where the panel's scope resolves.
  observeSurfaceFailure(DIFFS_UNAVAILABLE, () => props.address.kind === "turn" ? turnQuery.error() : undefined, () => props.projectId);
  observeSurfaceFailure(DIFFS_UNAVAILABLE, () => props.address.kind === "git" ? gitQuery.error() : undefined, () => props.projectId);
  observeSurfaceFailure(DIFFS_UNAVAILABLE, error, () => props.projectId);

  const rowContext = {
    client: () => props.client,
    projectId: () => props.projectId,
    address: () => props.address,
    markUserEdits,
    rootRefs: () => props.rootRefs,
  };

  // Section heights are premeasured by the host before layout.
  const digests = createDiffsDigests({
    client: () => props.client,
    projectId: () => props.projectId,
    sessionId: () => {
      void lensRevision();
      switch (props.address.kind) {
        case "turn": return props.address.sessionId;
        case "lens": return scopeSessionId(props.projectId);
        case "git": return undefined;
      }
    },
    requests: createMemo(() =>
      files().map((row) => ({ key: diffsFileKey(row), selector: diffsFileNetSelector(rowContext, row), fact: JSON.stringify(row.file) }))),
  });

  let sources = new Map<string, { fact: string; source: DiffRowSource }>();
  const sourceFor = createMemo(() => {
    const next = new Map<string, { fact: string; source: DiffRowSource }>();
    for (const row of files()) {
      const key = diffsFileKey(row), fact = JSON.stringify(row.file);
      const held = sources.get(key);
      next.set(key, held?.fact === fact ? held : { fact, source: diffsFileRowSource(rowContext, row, () => digests.digest(key)) });
    }
    sources = next;
    return (row: DiffsFile) => next.get(diffsFileKey(row))?.source
      ?? diffsFileRowSource(rowContext, row, () => digests.digest(diffsFileKey(row)));
  });

  const sections = createMemo(() => files().map((row) => {
    const key = diffsFileKey(row);
    return { key, source: sourceFor()(row), digest: digests.digest(key) };
  }));
  const sectionByKey = createMemo(() => new Map(sections().map((section) => [section.key, section])));
  const sourceByKey = createMemo(() => new Map(sections().map((section) => [section.key, section.source])));
  const total = createMemo(() => diffsTotal(sections().map((section) => ({
    key: section.key, digest: section.digest, open: true, window: [],
  }))));

  const isOpen = (key: string): boolean => !collapsed().has(key);
  const revisionOf = (key: string): number | null => revisions().get(key) ?? null;

  function toggle(key: string): void {
    setCollapsed((held) => {
      const next = new Set(held);
      if (next.has(key)) next.delete(key); else next.add(key);
      return next;
    });
  }

  function foldEvery(open: boolean): void {
    batch(() => {
      setAllOpen(open);
      setCollapsed(open ? new Set<string>() : new Set(sections().map((section) => section.key)));
    });
  }

  function chooseRevision(key: string, revision: number | null): void {
    setRevisions((held) => {
      const next = new Map(held);
      if (revision === null) next.delete(key); else next.set(key, revision);
      return next;
    });
    reader.reload(key);
  }

  const facts = createReaderFacts(
    () => sectionByKey().get(reader.factsSection)?.source.path(revisionOf(reader.factsSection)) ?? "",
    () => {},
  );

  const displayPrefs = createMemo(() => ({ ...editorDisplayPrefs(), wordWrap: diffWordWrapPref() }));

  const reader = createDiffsReader({
    scope,
    sections,
    prefs: displayPrefs,
    open: isOpen,
    revision: revisionOf,
    onReloading: (key, active) => {
      setReloading((held) => {
        const next = new Set(held);
        if (active) next.add(key); else next.delete(key);
        return next;
      });
    },
    onError: (cause) => setError(cause instanceof Error ? cause.message : String(cause)),
    pageHeading: () => (
      <DiffsPageHeading
        title={copy().title}
        note={copy().note}
        reading={loading()}
        fileCount={files().length}
        total={total()}
        writes={props.address.kind === "git" ? null : writeCount(files().reduce((sum, row) => sum + diffsFileWriteCount(row), 0))}
        turnAddress={props.address.kind === "turn" ? props.address : null}
        onWalkTurn={props.onWalkTurn}
        foldable={files().length > 1}
        allOpen={allOpen()}
        onFoldAll={() => foldEvery(!allOpen())}
      />
    ),
    sectionHeading: (key) => (
      <Show keyed when={sourceByKey().get(key)}>
        {(source) => (
          <DiffsSectionHeader
            source={source}
            open={isOpen(key)}
            onToggle={() => toggle(key)}
            revision={revisionOf(key)}
            onRevision={(revision) => chooseRevision(key, revision)}
            reloading={reloading().has(key)}
          />
        )}
      </Show>
    ),
  });
  onCleanup(() => reader.destroy());

  createEffect(() => {
    const parent = host();
    if (!parent) return;
    untrack(() => reader.mount(parent, facts));
  });
  createEffect(() => { void sections(); void collapsed(); reader.refresh(); });
  createEffect(() => { reader.prefs(displayPrefs()); });

  const jumpToFile = (direction: 1 | -1) => {
    const list = sections();
    if (!list.length) return;
    const currentKey = reader.visibleSectionKey();
    const currentIndex = currentKey ? list.findIndex((s) => s.key === currentKey) : -1;
    let targetIndex = 0;
    if (direction === 1) {
      targetIndex = currentIndex >= 0 ? Math.min(list.length - 1, currentIndex + 1) : 0;
    } else {
      targetIndex = currentIndex > 0 ? currentIndex - 1 : 0;
    }
    const target = list[targetIndex];
    if (target) {
      reader.revealSection(target.key);
    }
  };

  // Retained pages stay mounted; only the displayed one answers page commands.
  const presence = useResidentPresence();
  createEffect(() => {
    if (presence() !== "active") return;
    onCleanup(registerCommandHandler("files.diffNextFile", () => jumpToFile(1)));
    onCleanup(registerCommandHandler("files.diffPrevFile", () => jumpToFile(-1)));
    onCleanup(registerCommandHandler("files.diffToggleCollapse", () => foldEvery(!allOpen())));
  });

  const comparisonIdentity = createMemo(() => {
    void lensRevision();
    const sessionId = props.address.kind === "lens" ? scopeSessionId(props.projectId) : undefined;
    const lensScope = props.address.kind === "lens" ? lens().scope : undefined;
    const lensComparisonOff = props.address.kind === "lens" ? lens().comparisonOff : undefined;
    return `${diffsAddressKey(props.address)}\u0000${copy().title}\u0000${sessionId}\u0000${lensScope}\u0000${lensComparisonOff}\u0000${markUserEdits()}`;
  });
  createEffect(() => {
    void comparisonIdentity();
    untrack(() => {
      setRevisions(new Map());
      setReloading(new Set<string>());
      foldEvery(true);
      reader.reset();
    });
  });

  return (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.diffsPage)}
      class="den-diffs-page"
      data-testid="diffs-page"
      data-address={props.address.kind}
    >
      {facts.card}
      <Show when={!unread() && !loading() && files().length === 0}>
        <p class="den-diffs-page__note" data-testid="diffs-page-empty">
          {props.address.kind === "lens"
            ? lensEmptyCopy(lens().scope, { comparisonOff: lens().comparisonOff, subjectTitle: lens().subjectTitle })
            : props.address.kind === "git"
              ? "These commits change no files in this folder."
              : "This turn changed no files."}
        </p>
      </Show>
      <div class="den-diffs-page__reader" ref={setHost} />
      <Show when={inventory().truncated}>
        <p class="den-diffs-page__note">
          {props.address.kind === "git"
            ? `These commits change more than ${DIFFS_FILE_CAP} files; this page lists the first ${DIFFS_FILE_CAP}.`
            : `This comparison covers more than ${DIFFS_FILE_CAP} files. Walk it to read the rest.`}
        </p>
      </Show>
      <div class="den-files-editor__status" data-files-ctx="no-menu" data-testid="diffs-page-status">
        <span class="den-files-editor__mode den-files-editor__mode--readonly" data-testid="diffs-editor-mode" aria-live="polite">
          <ThemeIcon slot="diff" size={13} />
          All diffs
        </span>
        <Show when={files().length > 0}>
          <span class="den-files-editor__status-detail" data-testid="diffs-status-files">
            {files().length === 1 ? "1 file" : `${files().length} files`}
          </span>
        </Show>
        <Show when={total()}>
          {(t) => (
            <span class="den-file-edit-diff-stat" data-testid="diffs-status-stat" aria-label={`${t().added} added, ${t().removed} removed`}>
              <span class="den-file-edit-diff-stat-add">+{t().added}</span>
              <span class="den-file-edit-diff-stat-del">−{t().removed}</span>
            </span>
          )}
        </Show>
        <span class="den-files-editor__status-spacer" />
        <button
          type="button"
          class="den-files-editor__status-toggle den-files-editor__status-wrap"
          data-testid="diffs-page-wrap-toggle"
          aria-pressed={diffWordWrapPref()}
          onClick={() => void saveDiffWordWrap(!diffWordWrapPref())}
        >
          Wrap
        </button>
      </div>
    </div>
  );
}
