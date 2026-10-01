import { contextAction } from "../context-actions.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {
  BlueprintSummary,
  ProjectRoot,
  WorkflowSummary,
} from "../../api/types.ts";
import { copyPathMenuItems } from "../copy-path-menu-items.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { absolutePathForBuffer } from "../../files/components/project-files-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { requestArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { blueprintLauncherPresentation } from "../../workflow/session-launcher-model.ts";
import { InlineRenameInput } from "../inline-rename/InlineRenameInput.tsx";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../ContextMenu.tsx";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { SessionWorkflowLauncher } from "../chatview/SessionWorkflowLauncher.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  BLUEPRINT_TITLE_MAX_LEN,
  type BlueprintListSortDir,
  type BlueprintListSortKey,
  blueprintKindLabel,
  blueprintListTitle,
  blueprintPathLabel,
  clampBlueprintList,
  defaultCompatibleWorkflow,
  nextBlueprintListSortDir,
  sortBlueprintSummaries,
  workflowPickerLabel,
} from "./project-blueprints-model.ts";
import {
  blueprintDeleteSummary,
  confirmDeleteBlueprints,
  deleteBlueprints,
} from "./project-blueprints-delete.ts";

type ProjectBlueprintLaunchTarget = {
  sessionId: string;
};

type Props = {
  projectId: string;
  appStore: AppStore;
  client?: LycaonClient | null;
  hasRepo: boolean;
  /** Blueprint paths resolve from the primary root. */
  roots?: readonly ProjectRoot[];
  onLaunchedSession?: (target: ProjectBlueprintLaunchTarget) => void;
  onCreateSupportingSession: (workflow: WorkflowSummary) => Promise<void>;
  back?: StageBack | null;
};

function sortGlyph(active: boolean, direction: BlueprintListSortDir): string {
  if (!active) return "";
  return direction === "asc" ? "↑" : "↓";
}

export function ProjectBlueprintsView(props: Props) {
  const client = () => props.client ?? getLycaonClient();
  const blueprintCopyPathItems = (path: string): ContextMenuItem[] => {
    const roots = props.roots ?? [];
    const root = roots.find((r) => r.is_primary) ?? roots[0];
    const abs = root ? absolutePathForBuffer(root.path, path) : null;
    return copyPathMenuItems({
      copyAbsolute: abs ? () => void copyTextToClipboard(abs) : null,
      copyRelative: () => void copyTextToClipboard(path),
      absoluteTestId: "project-blueprint-copy-path",
      relativeTestId: "project-blueprint-copy-relative-path",
    });
  };
  const [pickerFor, setPickerFor] = createSignal<string | null>(null);
  const [selectedWorkflow, setSelectedWorkflow] = createSignal("");
  const [busyPath, setBusyPath] = createSignal<string | null>(null);
  const [renamingPath, setRenamingPath] = createSignal<string | null>(null);
  const [rowMenu, setRowMenu] = createSignal<{
    bp: BlueprintSummary;
    anchor: ContextMenuAnchor;
  } | null>(null);
  const [launcherBusy, setLauncherBusy] = createSignal(false);
  const [browseMoreOpen, setBrowseMoreOpen] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  const [selected, setSelected] = createSignal<Set<string>>(new Set<string>());
  /** Paths remaining in the delete batch. */
  const [deleteRun, setDeleteRun] = createSignal<{
    pending: ReadonlySet<string>;
    total: number;
  } | null>(null);
  const [deleteFailures, setDeleteFailures] = createSignal<
    ReadonlyMap<string, string>
  >(new Map<string, string>());
  const [listSortKey, setListSortKey] =
    createSignal<BlueprintListSortKey>("updated");
  const [listSortDir, setListSortDir] =
    createSignal<BlueprintListSortDir>("desc");
  let selectAllRef: HTMLInputElement | undefined;

  const blueprints = createSurfaceQuery({
    name: "project-blueprints",
    source: () => { const c = client(); return c ? { client: c, key: props.projectId } : null; },
    load: async ({ client, key }) => {
      const [items, catalog] = await Promise.all([client.listBlueprints(key), client.listWorkflows({ projectId: key })]);
      return { items: clampBlueprintList(items ?? []), catalog: catalog ?? [] };
    },
    required: false,
  });
  const refetchList = blueprints.refresh;

  const items = createMemo(() => blueprints.value()?.items ?? []);
  const sortedItems = createMemo(() =>
    sortBlueprintSummaries(items(), listSortKey(), listSortDir()),
  );
  const catalogRows = createMemo(() => blueprints.value()?.catalog ?? []);
  const launcher = createMemo(() =>
    blueprintLauncherPresentation(catalogRows(), { hasRepo: props.hasRepo }),
  );
  const resourcesSettled = () => blueprints.value() !== undefined;
  const resourceError = blueprints.error;
  const resourcesPending = blueprints.coldPending;
  const resourcesShowLoading = blueprints.showLoading;

  const selectedCount = () => selected().size;
  const allSelected = () => {
    const rows = sortedItems();
    return rows.length > 0 && rows.every((bp) => selected().has(bp.path));
  };

  const deleting = () => deleteRun() != null;
  const deletePending = (path: string) => deleteRun()?.pending.has(path) ?? false;
  const deleteSettled = () => {
    const run = deleteRun();
    return run ? run.total - run.pending.size : 0;
  };

  createEffect(() => {
    const alive = new Set(items().map((bp) => bp.path));
    setSelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      for (const id of prev) {
        if (alive.has(id)) next.add(id);
        else changed = true;
      }
      return changed ? next : prev;
    });
    setDeleteFailures((prev) => {
      let changed = false;
      const next = new Map<string, string>();
      for (const [path, reason] of prev) {
        if (alive.has(path)) next.set(path, reason);
        else changed = true;
      }
      return changed ? next : prev;
    });
  });

  createEffect(() => {
    const el = selectAllRef;
    if (!el) return;
    const some = selectedCount() > 0;
    el.indeterminate = some && !allSelected();
  });

  const toggleListSort = (key: BlueprintListSortKey) => {
    setListSortDir(nextBlueprintListSortDir(listSortKey(), key, listSortDir()));
    setListSortKey(key);
  };

  const toggleSelect = (path: string, on: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(path);
      else next.delete(path);
      return next;
    });
  };

  const toggleSelectAll = () => {
    const rows = sortedItems();
    setSelected((prev) => {
      const next = new Set(prev);
      if (allSelected()) {
        for (const bp of rows) next.delete(bp.path);
      } else {
        for (const bp of rows) next.add(bp.path);
      }
      return next;
    });
  };

  const selectAllBlueprints = () => {
    setSelected(new Set(sortedItems().map((bp) => bp.path)));
  };

  const clearSelection = () => setSelected(new Set<string>());

  const openRun = (bp: BlueprintSummary) => {
    setError(null);
    const ids = bp.compatible_workflows ?? [];
    const def = defaultCompatibleWorkflow(ids);
    if (!def) {
      setError("No compatible workflows for this blueprint.");
      return;
    }
    if (ids.length === 1) {
      void runLaunch(bp, def);
      return;
    }
    setSelectedWorkflow(def);
    setPickerFor(bp.path);
  };

  const runLaunch = async (bp: BlueprintSummary, workflowId: string) => {
    const c = client();
    if (!c) {
      setError("The app is still starting. Try again in a moment.");
      return;
    }
    const wf = catalogRows().find((row) => row.id === workflowId);
    if (!wf) {
      setError(`Workflow ${workflowId} is not in the catalog.`);
      return;
    }
    setBusyPath(bp.path);
    setError(null);
    try {
      const res = await c.launchBlueprint(props.projectId, bp.id, {
        target_workflow_id: workflowId,
        defer_start: true,
      });
      setPickerFor(null);
      requestArmWorkflow({ sessionId: res.session_id, workflow: wf });
      props.onLaunchedSession?.({ sessionId: res.session_id });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Launch failed.");
    } finally {
      setBusyPath(null);
    }
  };

  const armSupportingWorkflow = async (wf: WorkflowSummary) => {
    setLauncherBusy(true);
    setBrowseMoreOpen(false);
    setError(null);
    try {
      await props.onCreateSupportingSession(wf);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Could not open session.",
      );
    } finally {
      setLauncherBusy(false);
    }
  };

  const commitRename = async (bp: BlueprintSummary, nextTitle: string) => {
    const c = client();
    setRenamingPath(null);
    if (!c) {
      setError("The app is still starting. Try again in a moment.");
      return;
    }
    setBusyPath(bp.path);
    setError(null);
    try {
      await c.updateBlueprint(props.projectId, bp.id, { title: nextTitle });
      await refetchList();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Rename failed.");
    } finally {
      setBusyPath(null);
    }
  };

  const rowLabel = (path: string) => {
    const bp = items().find((row) => row.path === path);
    return bp ? blueprintListTitle(bp) : path;
  };

  const runDelete = async (
    targets: readonly BlueprintSummary[],
    confirmTitle?: string,
  ) => {
    if (deleting() || targets.length === 0) return;
    const paths = targets.map((bp) => bp.path);
    const c = client();
    if (!c) {
      setError("The app is still starting. Try again in a moment.");
      return;
    }
    if (!(await confirmDeleteBlueprints(paths.length, confirmTitle))) return;

    const batch = new Set(paths);
    setError(null);
    setDeleteFailures((prev) => {
      const next = new Map(prev);
      for (const path of batch) next.delete(path);
      return next;
    });
    const picking = pickerFor();
    if (picking && batch.has(picking)) setPickerFor(null);
    const renaming = renamingPath();
    if (renaming && batch.has(renaming)) setRenamingPath(null);
    setDeleteRun({ pending: batch, total: paths.length });

    const report = await deleteBlueprints(
      c,
      props.projectId,
      targets,
      (outcome) => {
        setDeleteRun((run) => {
          if (!run) return run;
          const pending = new Set(run.pending);
          pending.delete(outcome.path);
          return { pending, total: run.total };
        });
        if (!outcome.ok) {
          setDeleteFailures((prev) =>
            new Map(prev).set(outcome.path, outcome.reason),
          );
          return;
        }
        setSelected((prev) => {
          if (!prev.has(outcome.path)) return prev;
          const next = new Set(prev);
          next.delete(outcome.path);
          return next;
        });
      },
    );

    setDeleteRun(null);
    setError(blueprintDeleteSummary(report, rowLabel));
    await refetchList();
  };

  const deleteBlueprint = (bp: BlueprintSummary) =>
    runDelete([bp], blueprintListTitle(bp));

  const bulkDelete = () => {
    const chosen = selected();
    // Follow the displayed row order.
    return runDelete(
      sortedItems().filter((bp) => chosen.has(bp.path)),
    );
  };

  const updatedLabel = (bp: BlueprintSummary) => {
    const ms = Date.parse(bp.updated_at);
    return Number.isFinite(ms) ? formatRelativeTime(ms) : "—";
  };

  return (
    <div
      class="project-blueprints-view"
      data-testid="project-blueprints-view"
      aria-busy={resourcesPending()}
    >
      <BrowseStagePanel
        back={props.back}
        appStore={props.appStore}
        scrollHost="content"
        contentReady={() => resourcesSettled() || resourceError() !== undefined}
        title="Blueprints"
        error={error() ?? resourceError()}
        errorTestId="project-blueprints-error"
      >
        <div class="den-blueprints-stage">
          <Scrollport class="den-blueprints-body" contentClass="den-blueprints-body__content">
            <Show when={resourcesShowLoading()}>
              <p class="den-blueprints-empty" data-testid="project-blueprints-loading">
                Loading blueprints…
              </p>
            </Show>
            <Show when={resourcesSettled() && launcher().tiles.length > 0}>
              <div
                class="den-blueprints-launcher"
                data-testid="project-blueprints-launcher"
              >
                <SessionWorkflowLauncher
                  tiles={launcher().tiles}
                  busy={launcherBusy()}
                  heading="Pick a supporting workflow"
                  testId="project-blueprints-workflow-launcher"
                  onSelect={(wf) => void armSupportingWorkflow(wf)}
                  onBrowseAll={
                    launcher().more.length > 0
                      ? () => setBrowseMoreOpen((v) => !v)
                      : undefined
                  }
                />
                <Show when={browseMoreOpen() && launcher().more.length > 0}>
                  <div
                    class="den-blueprints-launcher-more"
                    data-testid="project-blueprints-launcher-more"
                  >
                    <div class="den-blueprints-launcher-more__heading">
                      More supporting workflows
                    </div>
                    <Scrollport class="den-blueprints-launcher-more__list" contentAs="ul" contentClass="den-blueprints-launcher-more__rows">
                      <For each={launcher().more}>
                        {(tile) => (
                          <li>
                            <button
                              type="button"
                              class="den-blueprints-launcher-more__row"
                              disabled={tile.disabled || launcherBusy()}
                              data-tip={
                                tile.disabled
                                  ? tile.disabledReason
                                  : undefined
                              }
                              data-testid={`project-blueprints-more-${tile.workflow.id}`}
                              onClick={() =>
                                void armSupportingWorkflow(tile.workflow)
                              }
                            >
                              <span class="den-blueprints-launcher-more__title">
                                {tile.title}
                              </span>
                              <Show when={tile.description}>
                                {(desc) => (
                                  <span class="den-blueprints-launcher-more__desc">
                                    {desc()}
                                  </span>
                                )}
                              </Show>
                            </button>
                          </li>
                        )}
                      </For>
                    </Scrollport>
                  </div>
                </Show>
              </div>
            </Show>
            <Show when={resourcesSettled() && items().length === 0}>
              <p class="den-blueprints-empty" data-testid="project-blueprints-empty">
                No blueprints yet. Start a supporting workflow above — its
                governing file will appear here.
              </p>
            </Show>
            <Show when={resourcesSettled() && items().length > 0}>
              <div
                class="den-list-panel den-blueprints-list-panel"
                data-testid="project-blueprints-list"
              >
                <div class="den-list-head">
                  <label class="project-list-check">
                    <DenCheckboxControl
                      ref={selectAllRef}
                      checked={allSelected()}
                      aria-label="Select all blueprints"
                      data-testid="project-blueprints-select-all"
                      onChange={toggleSelectAll}
                    />
                  </label>
                  <button
                    type="button"
                    class="den-list-sort"
                    classList={{
                      "den-list-sort--active": listSortKey() === "title",
                    }}
                    aria-sort={
                      listSortKey() === "title"
                        ? listSortDir() === "asc"
                          ? "ascending"
                          : "descending"
                        : "none"
                    }
                    data-testid="project-blueprints-sort-title"
                    onClick={() => toggleListSort("title")}
                  >
                    Title
                    <span class="den-list-sort-glyph" aria-hidden="true">
                      {sortGlyph(listSortKey() === "title", listSortDir())}
                    </span>
                  </button>
                  <span class="den-blueprints-list-head-kind">Kind</span>
                  <button
                    type="button"
                    class="den-list-sort den-list-sort--activity"
                    classList={{
                      "den-list-sort--active":
                        listSortKey() === "updated",
                    }}
                    aria-sort={
                      listSortKey() === "updated"
                        ? listSortDir() === "asc"
                          ? "ascending"
                          : "descending"
                        : "none"
                    }
                    data-testid="project-blueprints-sort-updated"
                    onClick={() => toggleListSort("updated")}
                  >
                    Updated
                    <span class="den-list-sort-glyph" aria-hidden="true">
                      {sortGlyph(listSortKey() === "updated", listSortDir())}
                    </span>
                  </button>
                  <span class="den-blueprints-list-head-actions" aria-hidden="true" />
                </div>
                <Show when={selectedCount() > 0}>
                  <div
                    class="den-list-toolbar"
                    data-testid="project-blueprints-toolbar"
                  >
                    {/* Announce each delete phase once. */}
                    <span
                      class="den-list-toolbar-count"
                      data-testid="project-blueprints-toolbar-count"
                      aria-live="polite"
                    >
                      <Show
                        when={deleting()}
                        fallback={`${selectedCount()} selected`}
                      >
                        Deleting…
                      </Show>
                    </span>
                    <Show when={deleteRun()}>
                      {(run) => (
                        <span
                          class="den-list-toolbar-progress"
                          aria-hidden="true"
                          data-testid="project-blueprints-delete-progress"
                        >
                          {Math.min(deleteSettled() + 1, run().total)} of{" "}
                          {run().total}
                        </span>
                      )}
                    </Show>
                    <Show
                      when={
                        !deleting() && selectedCount() < items().length
                      }
                    >
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="project-blueprints-select-all-rows"
                        onClick={selectAllBlueprints}
                      >
                        Select all {items().length}
                      </DenButton>
                    </Show>
                    <div class="den-list-toolbar-actions">
                      <DenButton
                        variant="ghost"
                        compact
                        disabled={deleting()}
                        data-testid="project-blueprints-clear-selection"
                        onClick={clearSelection}
                      >
                        Clear
                      </DenButton>
                      <DenButton
                        variant="danger"
                        compact
                        disabled={deleting()}
                        data-testid="project-blueprints-bulk-delete"
                        onClick={() => void bulkDelete()}
                      >
                        Delete
                      </DenButton>
                    </div>
                  </div>
                </Show>
                <ul class="den-list">
                  <For each={sortedItems()}>
                    {(bp) => {
                      const picking = () => pickerFor() === bp.path;
                      const pendingDelete = () => deletePending(bp.path);
                      const busy = () => busyPath() === bp.path || pendingDelete();
                      const renaming = () => renamingPath() === bp.path;
                      const isSelected = () => selected().has(bp.path);
                      const menuOpen = () => rowMenu()?.bp.path === bp.path;
                      const deleteFailure = () => deleteFailures().get(bp.path);
                      return (
                        <li
                          class="den-blueprints-row"
                          classList={{
                            "den-blueprints-row--selected": isSelected(),
                            "den-blueprints-row--picking": picking(),
                            "den-blueprints-row--pending": pendingDelete(),
                            "den-blueprints-row--failed": Boolean(deleteFailure()),
                          }}
                          aria-busy={pendingDelete() || undefined}
                          data-testid="project-blueprint-row"
                          data-blueprint-path={bp.path}
                        >
                          <label class="project-list-check">
                            <DenCheckboxControl
                              checked={isSelected()}
                              aria-label={`Select ${blueprintListTitle(bp)}`}
                              data-testid="project-blueprint-select"
                              onChange={(e) =>
                                toggleSelect(bp.path, e.currentTarget.checked)
                              }
                            />
                          </label>
                          <div class="den-blueprints-row__meta">
                            <Show
                              when={!renaming()}
                              fallback={
                                <InlineRenameInput
                                  class="den-blueprints-row__rename"
                                  testId="blueprint-rename-input"
                                  initialValue={
                                    bp.title?.trim() || blueprintListTitle(bp)
                                  }
                                  maxLength={BLUEPRINT_TITLE_MAX_LEN}
                                  ariaLabel="Rename blueprint"
                                  onCommit={(next) => void commitRename(bp, next)}
                                  onCancel={() => setRenamingPath(null)}
                                />
                              }
                            >
                              <span class="den-blueprints-row__title">
                                {blueprintListTitle(bp)}
                              </span>
                            </Show>
                            <Show when={blueprintPathLabel(bp)}>
                              {(path) => (
                                <span class="den-blueprints-row__path">{path()}</span>
                              )}
                            </Show>
                            <Show when={deleteFailure()}>
                              {(reason) => (
                                <span
                                  class="den-blueprints-row__failure"
                                  data-tip={reason()}
                                  data-tip-when-clipped
                                  data-testid="project-blueprint-delete-failure"
                                >
                                  Not deleted — {reason()}
                                </span>
                              )}
                            </Show>
                          </div>
                          <span class="den-blueprints-row__kind">
                            {blueprintKindLabel(bp)}
                          </span>
                          <span class="den-blueprints-row__when">
                            {updatedLabel(bp)}
                          </span>
                          <div class="den-blueprints-row__actions">
                            <Show
                              when={picking()}
                              fallback={
                                <button
                                  type="button"
                                  class="den-blueprints-run"
                                  data-testid="project-blueprint-run"
                                  disabled={busy() || renaming()}
                                  onClick={() => openRun(bp)}
                                >
                                  Run
                                </button>
                              }
                            >
                              <label class="den-blueprints-picker">
                                <span class="den-blueprints-picker__label">Workflow</span>
                                <DenSelect
                                  aria-label="Workflow"
                                  data-testid="project-blueprint-workflow-picker"
                                  value={selectedWorkflow()}
                                  options={(bp.compatible_workflows ?? []).map((id) => ({
                                    value: id,
                                    label: workflowPickerLabel(id, catalogRows()),
                                  }))}
                                  onValueChange={setSelectedWorkflow}
                                />
                              </label>
                              <button
                                type="button"
                                class="den-blueprints-run"
                                data-testid="project-blueprint-launch"
                                disabled={busy() || !selectedWorkflow().trim()}
                                onClick={() =>
                                  void runLaunch(bp, selectedWorkflow().trim())
                                }
                              >
                                Start
                              </button>
                              <button
                                type="button"
                                class="den-blueprints-cancel"
                                data-testid="project-blueprint-cancel-picker"
                                disabled={busy()}
                                onClick={() => setPickerFor(null)}
                              >
                                Cancel
                              </button>
                            </Show>
                          </div>
                          <div>
                            <button
                              type="button"
                              class="den-blueprints-row__menu-btn den-inset-icon-btn"
                              aria-label="Blueprint actions"
                              aria-haspopup="menu"
                              aria-expanded={menuOpen()}
                              data-testid="project-blueprint-menu"
                              disabled={busy() || deleting()}
                              onPointerDown={(e) => {
                                // The open menu stays closed for this pointer cycle.
                                if (menuOpen()) e.stopPropagation();
                              }}
                              onClick={(e) => {
                                if (menuOpen()) {
                                  setRowMenu(null);
                                  return;
                                }
                                const rect = e.currentTarget.getBoundingClientRect();
                                setRowMenu({
                                  bp,
                                  anchor: {
                                    x: rect.right,
                                    y: rect.bottom + 4,
                                  },
                                });
                              }}
                            >
                              <ThemeIcon slot="more" size={15} />
                            </button>
                          </div>
                        </li>
                      );
                    }}
                  </For>
                </ul>
              </div>
            </Show>
          </Scrollport>
        </div>
      </BrowseStagePanel>
      <Show when={rowMenu()} keyed>
        {(m) => (
          <ContextMenu
            anchor={m.anchor}
            align="end"
            onDismiss={() => setRowMenu(null)}
            items={[
              contextAction("rename", {
                testId: "project-blueprint-rename",
                onSelect: () => {
                  setPickerFor(null);
                  setRenamingPath(m.bp.path);
                },
              }),
              ...blueprintCopyPathItems(m.bp.path),
              contextAction("delete", {
                testId: "project-blueprint-delete",

                onSelect: () => {
                  void deleteBlueprint(m.bp);
                },
              }),
            ]}
          />
        )}
      </Show>
    </div>
  );
}
