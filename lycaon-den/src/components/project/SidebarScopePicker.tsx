import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { SourcePin } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { BrowseSegmented } from "../browse/BrowseSegmented.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import type { DeletedLines } from "../source/diff/scope-diff.ts";
import {
  lensPickerLabel,
  unpresentedAgentFileCount,
  type ReviewLensScope,
} from "../../files/review/review-model.ts";
import {
  chooseSidebarScope,
  getDeletedLines,
  getMarkMyEdits,
  getSidebarScope,
  hydrateReviewPinLabel,
  isComparisonOff,
  reviewScopeAvailable,
  setComparisonOff,
  setDeletedLines,
  setMarkMyEdits,
  subscribeDeletedLines,
  subscribeReviewScope,
  subscribeScopePickerRequest,
} from "../../files/review/review-pane.ts";
import {
  resolvedScope,
  subscribeResolvedScope,
} from "../../files/tree/scope-resolution.ts";
import {
  type LoadState,
  errorOf,
  loadFailed,
  loaded,
  loading,
  settledEmpty,
  unloaded,
  valueOf,
} from "../../store/load-state.ts";
import { leaveWalk } from "../../files/walk/walk-store.ts";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";

type Props = {
  projectId: string;
  client: LycaonClient | null;
  appStore: AppStore;
};

/** Reads the selected chat and its host turn ordinal, including turn zero. */
export function currentTurnFromStore(appStore: AppStore): {
  sessionId: string;
  turn: number;
  title: string;
} | null {
  const session = appStore.state.currentSession;
  const sessionId = session?.id.trim();
  if (!sessionId) return null;
  return {
    sessionId,
    turn: session?.current_turn ?? 0,
    title: session?.title?.trim() || "",
  };
}

/** Tooltip naming the recorded git position the pin was taken at. */
function pinGitTitle(pin: SourcePin): string | undefined {
  const parts = (pin.git_heads ?? [])
    .filter((head) => head.repo_state === "repo" && head.head_commit?.trim())
    .map((head) => {
      const commit = (head.head_commit ?? "").trim().slice(0, 8);
      const branch = head.head_ref?.trim();
      return branch ? `${commit} on ${branch}` : commit;
    });
  return parts.length > 0 ? `Taken at ${parts.join(" · ")}` : undefined;
}

function snapshotLabel(now = new Date()): string {
  const timestamp = new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(now);
  return `Snapshot ${timestamp}`;
}

export function SidebarScopePicker(props: Props) {
  const [paneTick, setPaneTick] = createSignal(0);
  const [scopeTick, setScopeTick] = createSignal(0);
  const [pickerOpen, setPickerOpen] = createSignal(false);
  onCleanup(subscribeScopePickerRequest((projectId) => {
    if (projectId === props.projectId.trim()) setPickerOpen(true);
  }));
  const [pinsState, setPinsState] = createSignal<LoadState<SourcePin[]>>(unloaded());
  /** Resolved pins, or an empty render list while loading. */
  const pins = () => valueOf(pinsState()) ?? [];
  const mutatePins = (mutate: (current: SourcePin[]) => SourcePin[]) => {
    setPinsState((current) => loaded(mutate(valueOf(current) ?? [])));
  };
  const [nextPinCursor, setNextPinCursor] = createSignal("");
  const [pinsLoading, setPinsLoading] = createSignal(false);
  const [pinCreating, setPinCreating] = createSignal(false);
  const [pinError, setPinError] = createSignal<string | null>(null);
  let triggerEl: HTMLButtonElement | undefined;
  let pickerEl: HTMLDivElement | undefined;

  createAnchoredPopoverFocus(
    pickerOpen,
    () => pickerEl,
    {
      trigger: () => triggerEl,
      onEscape: () => setPickerOpen(false),
    },
  );

  onCleanup(
    subscribeReviewScope((id) => {
      if (id === props.projectId.trim()) setPaneTick((n) => n + 1);
    }),
  );
  onCleanup(
    subscribeResolvedScope((id) => {
      if (id === props.projectId.trim()) setScopeTick((n) => n + 1);
    }),
  );

  const scope = createMemo((): ReviewLensScope => {
    void paneTick();
    return getSidebarScope(props.projectId);
  });

  const comparisonOff = createMemo(() => {
    void paneTick();
    return isComparisonOff(props.projectId);
  });

  createEffect(() => {
    const selected = scope();
    if (selected.kind !== "pin") return;
    const pin = pins().find((entry) =>
      entry.project_id === props.projectId.trim() && entry.id === selected.pinId);
    if (pin) hydrateReviewPinLabel(props.projectId, pin.id, pin.label?.trim() || "Untitled pin");
  });

  const [deletedLinesTick, setDeletedLinesTick] = createSignal(0);
  onCleanup(
    subscribeDeletedLines((id) => {
      if (id === props.projectId.trim()) setDeletedLinesTick((n) => n + 1);
    }),
  );
  const deletedLines = createMemo((): DeletedLines => {
    void deletedLinesTick();
    return getDeletedLines(props.projectId);
  });

  const markMyEdits = createMemo(() => {
    void paneTick();
    return getMarkMyEdits(props.projectId);
  });
  /** Git records no author, so the commit comparison marks every change. */
  const markingNeedsAuthors = createMemo(() => scope().kind === "commit");

  /** Chat comparisons read the chat selected in the sidebar. */
  const selectedChat = createMemo(() => {
    void props.appStore.state.currentSession?.id;
    void props.appStore.state.currentSession?.title;
    return currentTurnFromStore(props.appStore);
  });

  const label = createMemo(() =>
    lensPickerLabel(scope(), { comparisonOff: comparisonOff() }),
  );

  const subjectTitle = createMemo(() => selectedChat()?.title ?? "");

  /** Counts agent changes not yet presented by the file surfaces. */
  const newFromAI = createMemo(() => {
    void scopeTick();
    if (comparisonOff()) return 0;
    return unpresentedAgentFileCount(resolvedScope(props.projectId).files);
  });

  const scopeDescription = createMemo(() => {
    const title = subjectTitle();
    return title
      ? `Choose what the dots mark. Chat options compare against “${title}”, the chat selected in the sidebar.`
      : "Choose what the dots mark. The Review tab uses the same comparison.";
  });

  const commitAvailable = createMemo(() => {
    void scopeTick();
    return resolvedScope(props.projectId).commitAvailable;
  });
  const availability = createMemo(() => ({ chatSelected: selectedChat() != null, commitAvailable: commitAvailable() }));
  const offered = (scope: ReviewLensScope) => reviewScopeAvailable(scope, availability());

  createEffect(() => {
    const c = props.client;
    const pid = props.projectId;
    if (!c || !pid.trim()) {
      setPinsState(() => unloaded<SourcePin[]>());
      setNextPinCursor("");
      return;
    }
    // Reset pin state when the project changes.
    setPinsState(() => loading<SourcePin[]>());
    setNextPinCursor("");
    let cancelled = false;
    void c.listProjectSourcePins(pid).then(
      (res) => {
        if (!cancelled) {
          setPinsState(() => loaded(res.pins));
          setNextPinCursor(res.next_cursor ?? "");
        }
      },
      (err) => {
        if (!cancelled) setPinsState(() => loadFailed<SourcePin[]>(err));
      },
    );
    onCleanup(() => {
      cancelled = true;
    });
  });

  const loadOlderPins = async () => {
    const c = props.client;
    const projectId = props.projectId.trim();
    const cursor = nextPinCursor();
    if (!c || !projectId || !cursor || pinsLoading()) return;
    setPinsLoading(true);
    setPinError(null);
    try {
      const page = await c.listProjectSourcePins(projectId, { cursor });
      if (props.projectId.trim() !== projectId) return;
      mutatePins((current) => {
        const ids = new Set(current.map((pin) => pin.id));
        return [...current, ...page.pins.filter((pin) => !ids.has(pin.id))];
      });
      setNextPinCursor(page.next_cursor ?? "");
    } catch {
      setPinError("Couldn’t load older pins. Try again.");
    } finally {
      setPinsLoading(false);
    }
  };

  const applyScope = (next: ReviewLensScope) => {
    chooseSidebarScope(props.projectId, next);
  };

  const pickTurn = () => {
    if (!selectedChat()) return;
    applyScope({ kind: "turn" });
  };

  const pickWholeChat = () => {
    if (!selectedChat()) return;
    applyScope({ kind: "session" });
  };

  const createPin = async () => {
    const c = props.client;
    if (!c || pinCreating()) return;
    setPinError(null);
    setPinCreating(true);
    try {
      const created = await c.createProjectSourcePin(props.projectId, {
        label: snapshotLabel(),
      });
      mutatePins((current) => [created, ...current]);
      applyScope({
        kind: "pin",
        pinId: created.id,
        label: created.label,
      });
    } catch (err) {
      setPinError(
        err instanceof LycaonApiError && err.code === "source_inventory_pending"
          ? "Repository history is still being indexed. Try again shortly."
          : "Couldn’t save this pin. Try again.",
      );
    } finally {
      setPinCreating(false);
    }
  };

  // The pin leaves the list now; a refused delete puts it back where it was.
  const removePin = async (pin: SourcePin) => {
    const c = props.client;
    if (!c) return;
    const index = (valueOf(pinsState()) ?? []).findIndex((entry) => entry.id === pin.id);
    if (index < 0) return;
    setPinError(null);
    mutatePins((current) => current.filter((entry) => entry.id !== pin.id));
    const activeScope = scope();
    if (activeScope.kind === "pin" && activeScope.pinId === pin.id) {
      const off = comparisonOff();
      applyScope({ kind: "new" });
      if (off) setComparisonOff(props.projectId, true);
    }
    try {
      await c.deleteProjectSourcePin(props.projectId, pin.id);
    } catch {
      mutatePins((current) =>
        current.some((entry) => entry.id === pin.id)
          ? current
          : [...current.slice(0, index), pin, ...current.slice(index)],
      );
      setPinError("Couldn’t remove this pin. Try again.");
    }
  };

  /** Off is mutually exclusive with comparison entries. */
  const scopeSelected = (of: (s: ReviewLensScope) => boolean) =>
    !comparisonOff() && of(scope());

  const turnOffComparison = () => {
    leaveWalk(props.projectId);
    setComparisonOff(props.projectId, true);
  };

  onMount(() => {
    requestFirstTimeTip("files-review-scope");
  });

  return (
    <div
      class="den-sidebar-scope-picker"
      data-files-ctx="no-menu"
    >
      <div class="den-sidebar-scope-picker__control">
        <button
          type="button"
          class="den-quiet-icon-btn"
          data-testid="sidebar-scope-picker"
          data-first-time-tip-anchor="files-review-scope"
          data-tip={`Scope: ${label()}`}
          data-tip-pos="below"
          aria-haspopup="dialog"
          aria-expanded={pickerOpen()}
          aria-controls="sidebar-scope-picker-menu"
          aria-label={`Scope: ${label()}`}
          ref={(element) => {
            triggerEl = element;
          }}
          onClick={() => {
            setPickerOpen((v) => !v);
          }}
        >
          <ThemeIcon
            slot={comparisonOff() ? "scope-off" : "scope"}
            class="den-sidebar-scope-picker__eye"
            data-testid="sidebar-scope-picker-eye"
            size={16}
          />
        </button>
        <Show when={pickerOpen()}>
          <AnchoredSurface
            ref={(element) => {
              pickerEl = element;
            }}
            id="sidebar-scope-picker-menu"
            class="den-sidebar-scope-picker__menu"
            role="dialog"
            ariaLabel="Review view"
            testId="sidebar-scope-picker-menu"
            anchor={() => triggerEl}
            preferredSide="bottom"
            align="end"
            overflow="hidden"
            onKeyDown={(event) => {
              if (event.metaKey || event.ctrlKey || event.altKey) return;
              if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement) return;
              switch (event.key.toLowerCase()) {
                case "t":
                  if (selectedChat()) {
                    event.preventDefault();
                    pickTurn();
                  }
                  break;
                case "c":
                  if (commitAvailable()) {
                    event.preventDefault();
                    applyScope({ kind: "commit" });
                  }
                  break;
                case "s":
                case "a":
                  if (selectedChat()) {
                    event.preventDefault();
                    pickWholeChat();
                  }
                  break;
                case "n":
                  event.preventDefault();
                  applyScope({ kind: "new" });
                  break;
                case "o":
                  event.preventDefault();
                  turnOffComparison();
                  setPickerOpen(false);
                  break;
                case "m":
                  if (!markingNeedsAuthors()) {
                    event.preventDefault();
                    setMarkMyEdits(props.projectId, !markMyEdits());
                  }
                  break;
                case "d":
                  event.preventDefault();
                  setDeletedLines(
                    props.projectId,
                    deletedLines() === "inplace" ? "folded" : "inplace",
                  );
                  break;
              }
            }}
            onDismiss={() => setPickerOpen(false)}
          >
            <Scrollport class="den-sidebar-scope-picker__menu-scroll" contentAs="ul" contentClass="den-sidebar-scope-picker__menu-list">
            <li
              class="den-sidebar-scope-picker__menu-heading"
              role="presentation"
            >
              <span>Review</span>
              <Show when={newFromAI() > 0}>
                <span
                  class="den-status-mark"
                  data-tone="accent"
                  data-testid="sidebar-scope-picker-new-agent"
                >
                  {newFromAI()} new from AI
                </span>
              </Show>
            </li>
            <li
              class="den-sidebar-scope-picker__menu-description"
              role="presentation"
              data-testid="sidebar-scope-picker-subject"
            >
              {scopeDescription()}
            </li>
            <li
              class="den-sidebar-scope-picker__section-label"
              role="presentation"
            >
              Compare with
            </li>
            <li>
              <button
                type="button"
                class="den-sidebar-scope-picker__menu-item"
                aria-pressed={scopeSelected((s) => s.kind === "new")}
                data-testid="sidebar-scope-opt-new"
                onClick={() => applyScope({ kind: "new" })}
              >
                <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true">
                  ✓
                </span>
                <span class="den-sidebar-scope-picker__menu-text">
                  New since you looked
                </span>
              </button>
            </li>
            <li>
              <button
                type="button"
                class="den-sidebar-scope-picker__menu-item"
                aria-pressed={scopeSelected((s) => s.kind === "turn")}
                disabled={!offered({ kind: "turn" })}
                data-testid="sidebar-scope-opt-turn"
                onClick={pickTurn}
              >
                <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true">
                  ✓
                </span>
                <span class="den-sidebar-scope-picker__menu-text">
                  This turn
                </span>
              </button>
            </li>
            <li>
              <button
                type="button"
                class="den-sidebar-scope-picker__menu-item"
                aria-pressed={scopeSelected((s) => s.kind === "session")}
                disabled={!offered({ kind: "session" })}
                data-testid="sidebar-scope-opt-chat"
                onClick={pickWholeChat}
              >
                <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true">
                  ✓
                </span>
                <span class="den-sidebar-scope-picker__menu-text">
                  Everything in this chat
                </span>
              </button>
            </li>
            <li>
              <button
                type="button"
                class="den-sidebar-scope-picker__menu-item"
                aria-pressed={scopeSelected((s) => s.kind === "commit")}
                disabled={!offered({ kind: "commit" })}
                data-tip={
                  commitAvailable()
                    ? "Compare with Git HEAD"
                    : "Git is unavailable; Review pins still work"
                }
                data-testid="sidebar-scope-opt-commit"
                onClick={() => applyScope({ kind: "commit" })}
              >
                <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true">
                  ✓
                </span>
                <span class="den-sidebar-scope-picker__menu-text">
                  Since last commit
                </span>
              </button>
            </li>

            <li
              class="den-sidebar-scope-picker__menu-separator"
              role="presentation"
            />
            <li>
              <label
                class="den-sidebar-scope-picker__option den-sidebar-scope-picker__option--toggle"
                classList={{
                  "den-sidebar-scope-picker__option--disabled": markingNeedsAuthors(),
                }}
                data-testid="sidebar-scope-mark-my-edits"
                data-tip={
                  markingNeedsAuthors()
                    ? "Git doesn’t record who made a change, so every change is marked."
                    : undefined
                }
                data-tip-pos="end"
              >
                <span class="den-sidebar-scope-picker__option-title">
                  Mark my edits
                </span>
                <DenCheckboxControl
                  checked={markMyEdits() || markingNeedsAuthors()}
                  disabled={markingNeedsAuthors()}
                  data-testid="sidebar-scope-mark-my-edits-control"
                  onChange={(e) => setMarkMyEdits(props.projectId, e.currentTarget.checked)}
                />
              </label>
            </li>
            <li
              class="den-sidebar-scope-picker__option"
              data-testid="sidebar-scope-deleted-lines"
            >
              <span class="den-sidebar-scope-picker__option-title">
                Deleted lines
              </span>
              <BrowseSegmented
                class="den-sidebar-scope-picker__segment"
                ariaLabel="Deleted lines"
                value={deletedLines()}
                options={[
                  { id: "folded", label: "Folded", testId: "sidebar-scope-deleted-lines-folded" },
                  { id: "inplace", label: "In place", testId: "sidebar-scope-deleted-lines-inplace" },
                ]}
                onChange={(id) => {
                  if (id === "folded" || id === "inplace") setDeletedLines(props.projectId, id);
                }}
              />
              <span class="den-sidebar-scope-picker__option-detail">
                {deletedLines() === "folded"
                  ? "Folded into a mark on the edge. Hover it to preview."
                  : "Shown above the lines that replaced them."}
              </span>
            </li>

            <li
              class="den-sidebar-scope-picker__section-label"
              role="presentation"
            >
              Pins
            </li>
            <li>
              <button
                type="button"
                class="den-sidebar-scope-picker__pin-create"
                data-testid="sidebar-scope-pin-current"
                disabled={!props.client || pinCreating()}
                onClick={() => void createPin()}
              >
                <span class="den-sidebar-scope-picker__pin-create-copy">
                  <span class="den-sidebar-scope-picker__pin-create-title">
                    {pinCreating() ? "Saving pin…" : "Pin current state"}
                  </span>
                  <span class="den-sidebar-scope-picker__pin-create-detail">
                    Compare with it any time
                  </span>
                </span>
              </button>
            </li>
            <Show when={pinError()}>
              <li
                class="den-sidebar-scope-picker__pin-error"
                role="status"
                data-testid="sidebar-scope-pin-error"
              >
                {pinError()}
              </li>
            </Show>
            <Show when={settledEmpty(pinsState(), (list) => list.length === 0)}>
              <li class="den-sidebar-scope-picker__menu-empty" role="presentation">
                No pins yet
              </li>
            </Show>
            <Show when={errorOf(pinsState()) !== undefined && pins().length === 0}>
              <li
                class="den-sidebar-scope-picker__pin-error"
                role="status"
                data-testid="sidebar-scope-pins-load-error"
              >
                Couldn’t load pins. Reopen this menu to retry.
              </li>
            </Show>
            <For each={pins()}>
              {(pin) => (
                <li class="den-sidebar-scope-picker__pin-row">
                  <button
                    type="button"
                    class="den-sidebar-scope-picker__menu-item"
                    aria-pressed={scopeSelected(
                      (s) =>
                        s.kind === "pin" &&
                        s.pinId === pin.id,
                    )}
                    data-testid={`sidebar-scope-opt-pin-${pin.id}`}
                    data-tip={pinGitTitle(pin)}
                    onClick={() =>
                      applyScope({
                        kind: "pin",
                        pinId: pin.id,
                        label: pin.label,
                      })
                    }
                  >
                    <span
                      class="den-sidebar-scope-picker__menu-check"
                      aria-hidden="true"
                    >
                      ✓
                    </span>
                    <span class="den-sidebar-scope-picker__menu-text">
                      {pin.label?.trim() || "Untitled pin"}
                    </span>
                  </button>
                  <button
                    type="button"
                    class="den-sidebar-scope-picker__pin-remove"
                    aria-label={`Remove pin “${pin.label?.trim() || "Untitled pin"}”`}
                    data-testid={`sidebar-scope-remove-pin-${pin.id}`}
                    onClick={() => void removePin(pin)}
                  >
                    <ThemeIcon slot="delete" size={14} />
                  </button>
                </li>
              )}
            </For>
            <Show when={nextPinCursor()}>
              <li>
                <button
                  type="button"
                  class="den-sidebar-scope-picker__menu-item"
                  data-testid="sidebar-scope-load-older-pins"
                  disabled={pinsLoading()}
                  onClick={() => void loadOlderPins()}
                >
                  <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true" />
                  <span class="den-sidebar-scope-picker__menu-text">
                    {pinsLoading() ? "Loading…" : "Load older pins"}
                  </span>
                </button>
              </li>
            </Show>

            <li
              class="den-sidebar-scope-picker__menu-separator"
              role="presentation"
            />
              <li>
                <button
                  type="button"
                  class="den-sidebar-scope-picker__menu-item den-sidebar-scope-picker__menu-item--off"
                  aria-pressed={comparisonOff()}
                  data-testid="sidebar-scope-opt-off"
                  onClick={turnOffComparison}
                >
                  <span class="den-sidebar-scope-picker__menu-check" aria-hidden="true">
                    ✓
                  </span>
                  <span class="den-sidebar-scope-picker__menu-text">
                    Turn off marking
                  </span>
                </button>
              </li>
            </Scrollport>
          </AnchoredSurface>
        </Show>
      </div>
    </div>
  );
}
