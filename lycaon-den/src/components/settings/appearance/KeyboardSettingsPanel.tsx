import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import { liveResolvedKeymap } from "../../../contributions/frame-keymap.ts";
import {
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import {
  COMMAND_SCOPE_LABEL,
  COMMAND_SCOPE_ORDER,
  commandDisplaySections,
  NAV_LEADER_DESCRIPTOR,
  shortcutDisplayEntries,
  type CommandDisplaySection,
} from "../../../shortcuts/command-display.ts";
import { NAV_LEADER_ID, type CommandScope } from "../../../shortcuts/keymap.ts";
import {
  displayBinding,
  eventToBareKey,
  eventToChord,
  serializeSequence,
} from "../../../shortcuts/chord.ts";
import { formatChordsDisplay } from "../../../shortcuts/display-binding-for.ts";
import {
  detectConflicts,
  detectSequenceConflicts,
  leaderShadowedBindings,
} from "../../../shortcuts/keymap.ts";
import { shortcutPlatform } from "../../../shortcuts/platform.ts";
import { contributionFrame } from "../../../contributions/contribution-store.ts";
import {
  clearShortcutOverride,
  resetAllShortcuts,
  saveShortcutOverride,
  shortcutOverrides,
  syncShortcutPrefsFromSnapshot,
} from "../../../settings/system/shortcut-prefs.ts";
import {
  setShortcutCaptureActive,
} from "../../../shortcuts/dispatcher.ts";
import { loadSharedAppState } from "../../../store/app-state-snapshot.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { Keycaps } from "../../shortcuts/Keycaps.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";

const SCOPE_HINT: Record<CommandScope, string> = {
  global: "Available throughout the app",
  composer: "When the message composer is focused",
  overlay: "Dialogs, help, and other overlays",
  files: "When the Files stage is open",
};

const KEYBOARD_TABS: readonly { id: CommandScope; label: string }[] =
  COMMAND_SCOPE_ORDER.map((id) => ({
    id,
    label: COMMAND_SCOPE_LABEL[id],
  }));

function sectionScope(section: CommandDisplaySection): CommandScope {
  return section.scope;
}

/** Normalizes chord text for search. */
function collapseChord(value: string): string {
  return value.toLowerCase().replace(/[\s+\-_/]/g, "");
}

/** Matches shortcut metadata and chord text. */
export function matchesShortcutQuery(
  query: string,
  cmd: {
    readonly id: string;
    readonly title: string;
    readonly commandId?: string;
    readonly provider?: string;
  },
  chords: readonly string[],
  display: string,
): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if (cmd.title.toLowerCase().includes(q)) return true;
  if (cmd.id.toLowerCase().includes(q)) return true;
  if (cmd.commandId?.toLowerCase().includes(q)) return true;
  if (cmd.provider?.toLowerCase().includes(q)) return true;
  const collapsed = collapseChord(q);
  if (!collapsed) return true;
  if (collapseChord(display).includes(collapsed)) return true;
  return chords.some((chord) => collapseChord(chord).includes(collapsed));
}

export function KeyboardSettingsPanel() {
  const [initialized, setInitialized] = createSignal(false);
  const [tab, setTab] = createSignal<CommandScope>("global");
  const [recordingId, setRecordingId] = createSignal<string | null>(
    null,
  );
  /** Leader waiting for its second key. */
  const [pendingLeader, setPendingLeader] = createSignal<string | null>(null);
  const [refuseReason, setRefuseReason] = createSignal<string | null>(null);
  const [query, setQuery] = createSignal("");

  onMount(() => {
    void (async () => {
      await loadSharedAppState();
      syncShortcutPrefsFromSnapshot();
      setInitialized(true);
    })();
  });

  const platform = () => shortcutPlatform();

  const resolved = createMemo(() =>
    liveResolvedKeymap(platform(), shortcutOverrides()),
  );

  const conflictIds = createMemo(() => {
    const ids = new Set<string>();
    for (const c of detectConflicts(resolved())) {
      for (const id of c.bindingIds) ids.add(id);
    }
    for (const c of detectSequenceConflicts(resolved())) {
      for (const id of c.bindingIds) ids.add(id);
    }
    for (const id of leaderShadowedBindings(resolved())) ids.add(id);
    return ids;
  });

  const overrideCount = createMemo(() => Object.keys(shortcutOverrides()).length);

  const chordsFor = (id: string): readonly string[] =>
    resolved().byDeclaration.get(id) ?? [];

  const bindingFor = (id: string): string =>
    formatChordsDisplay(chordsFor(id), platform());

  const hasOverride = (id: string) => id in shortcutOverrides();

  const leaderChord = (): string => resolved().leaderChord;

  const leaderDisplay = (): string =>
    formatChordsDisplay([leaderChord()], platform());

  const leaderMatchesQuery = (): boolean => {
    const chord = leaderChord();
    return matchesShortcutQuery(
      query(),
      NAV_LEADER_DESCRIPTOR,
      [chord],
      leaderDisplay(),
    );
  };

  const groupsForScope = (scope: CommandScope) => {
    const q = query();
    const plat = platform();
    return commandDisplaySections(shortcutDisplayEntries(contributionFrame()))
      .filter((section) => sectionScope(section) === scope)
      .map((section) => {
        const entries = section.entries.filter((entry) => {
          const chords = resolved().byDeclaration.get(entry.id) ?? [];
          return matchesShortcutQuery(
            q,
            entry,
            chords,
            formatChordsDisplay(chords, plat),
          );
        });
        return {
          id: section.id,
          label: section.label,
          // Only named groups need headings.
          showHead: section.group != null,
          testId: section.group
            ? `keyboard-group-${section.group}`
            : `keyboard-scope-${section.scope}`,
          entries,
        };
      })
      .filter((g) => g.entries.length > 0);
  };

  const groups = createMemo(() => groupsForScope(tab()));

  const tabHasMatches = (scope: CommandScope): boolean => {
    if (scope === "global" && leaderMatchesQuery()) return true;
    return groupsForScope(scope).length > 0;
  };

  // Keep the active tab on matching results.
  createEffect(() => {
    const q = query().trim();
    if (!q) return;
    if (tabHasMatches(tab())) return;
    const next = COMMAND_SCOPE_ORDER.find((scope) => tabHasMatches(scope));
    if (next) setTab(next);
  });

  /** Releases capture after the current event. */
  const releaseCaptureGate = () => {
    queueMicrotask(() => {
      if (recordingId() === null) setShortcutCaptureActive(false);
    });
  };

  const startRecording = (id: string) => {
    setRefuseReason(null);
    setPendingLeader(null);
    setRecordingId((cur) => {
      const next = cur === id ? null : id;
      // Capture starts before listeners mount.
      if (next !== null) setShortcutCaptureActive(true);
      return next;
    });
    const active = document.activeElement;
    if (
      active instanceof HTMLElement &&
      active.matches("input, textarea, select, [contenteditable='true']")
    ) {
      active.blur();
    }
  };

  createEffect(() => {
    const id = recordingId();
    if (!id) return;

    const swallow = (event: Event) => {
      event.preventDefault();
      event.stopPropagation();
      event.stopImmediatePropagation();
    };

    /** Validates and persists the captured binding. */
    const commit = (binding: string) => {
      setRefuseReason(null);
      setPendingLeader(null);
      setRecordingId(null);
      void (async () => {
        const result = await saveShortcutOverride(id, binding);
        if (!result.ok) setRefuseReason(result.reason);
      })();
    };

    const abandon = (reason: string) => {
      setPendingLeader(null);
      setRecordingId(null);
      setRefuseReason(reason);
    };

    const onKeyDown = (event: KeyboardEvent) => {
      swallow(event);
      if (event.repeat || event.isComposing) return;

      if (event.key === "Escape") {
        setRefuseReason(null);
        setPendingLeader(null);
        setRecordingId(null);
        return;
      }

      // Sequences accept one bare key.
      const leader = pendingLeader();
      if (leader !== null) {
        const secondKey = eventToBareKey(event);
        const sequence = secondKey
          ? serializeSequence(leader, secondKey)
          : null;
        if (!sequence) {
          abandon("The second key has to be a single key with no modifiers.");
          return;
        }
        commit(sequence);
        return;
      }

      const chord = eventToChord(event, platform());
      if (!chord) return;

      // The leader begins sequence capture.
      if (id !== NAV_LEADER_ID && chord === resolved().leaderChord) {
        setPendingLeader(chord);
        return;
      }

      commit(chord);
    };

    window.addEventListener("keydown", onKeyDown, true);
    window.addEventListener("keyup", swallow, true);
    onCleanup(() => {
      window.removeEventListener("keydown", onKeyDown, true);
      window.removeEventListener("keyup", swallow, true);
      releaseCaptureGate();
    });
  });

  return (
    <section class="den-settings-section" data-testid="keyboard-settings-panel">
      <header class="den-keyboard-header">
        <div class="den-keyboard-header__copy">
          <p class="den-settings-hint">
            Click a shortcut to rebind it. Changes apply immediately and stay on
            this device.
          </p>
        </div>
        <Show when={overrideCount() > 0}>
          <DenButton
            variant="ghost"
            compact
            data-testid="keyboard-reset-all"
            disabled={!initialized()}
            onClick={() => void resetAllShortcuts()}
          >
            Reset all
          </DenButton>
        </Show>
      </header>

      <DenInput
        class="den-keyboard-search"
        type="search"
        placeholder="Filter shortcuts…"
        value={query()}
        onInput={(e) => setQuery(e.currentTarget.value)}
        data-testid="keyboard-filter"
        aria-label={settingLabel("keyboard-shortcuts")}
        {...settingAnchor("keyboard-shortcuts")}
      />

      <UnderlineTabs aria-label="Shortcut scopes">
        <For each={KEYBOARD_TABS}>
          {(t) => (
            <button
              type="button"
              role="tab"
              id={`keyboard-tab-${t.id}`}
              aria-selected={tab() === t.id}
              aria-controls={`keyboard-panel-${t.id}`}
              class="den-underline-tab"
              data-active={tab() === t.id}
              data-testid={`keyboard-tab-${t.id}`}
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </UnderlineTabs>

      <Show when={refuseReason()}>
        <p class="den-keyboard-callout" data-testid="keyboard-reserved-refuse" role="status">
          {refuseReason()}
        </p>
      </Show>

      <div
        id={`keyboard-panel-${tab()}`}
        role="tabpanel"
        aria-labelledby={`keyboard-tab-${tab()}`}
        class="den-settings-section"
        data-testid={`keyboard-panel-${tab()}`}
      >
      <Show
        when={
          groups().length > 0 ||
          (tab() === "global" && leaderMatchesQuery())
        }
        fallback={
          <p class="den-settings-hint" data-testid="keyboard-filter-empty">
            No shortcuts match “{query().trim()}”.
          </p>
        }
      >
        <p class="den-settings-hint">{SCOPE_HINT[tab()]}</p>
        <Show when={tab() === "global" && leaderMatchesQuery()}>
          <section
            class="den-keyboard-section"
            data-testid="keyboard-group-leader"
          >
            <div class="den-keyboard-section__head">
              <h3 class="den-keyboard-section__title" {...chromeProps()}>Leader key</h3>
              <p class="den-keyboard-section__hint">
                Prefix for two-step sequences.
              </p>
            </div>
            <ul class="den-keyboard-list">
              <li
                class="den-keyboard-row"
                classList={{
                  "den-keyboard-row--recording": recordingId() === NAV_LEADER_ID,
                }}
                data-testid={`keyboard-row-${NAV_LEADER_ID}`}
                data-command={NAV_LEADER_ID}
              >
                <div class="den-keyboard-row__meta">
                  <span class="den-keyboard-row__label">
                    {NAV_LEADER_DESCRIPTOR.title}
                  </span>
                  <Show when={hasOverride(NAV_LEADER_ID)}>
                    <span
                      class="den-keyboard-row__badge den-keyboard-row__badge--custom"
                      data-testid={`keyboard-custom-${NAV_LEADER_ID}`}
                    >
                      Custom
                    </span>
                  </Show>
                </div>
                <div class="den-keyboard-row__actions">
                  <button
                    type="button"
                    class="den-keyboard-bind"
                    classList={{
                      "den-keyboard-bind--recording":
                        recordingId() === NAV_LEADER_ID,
                    }}
                    data-testid={`keyboard-record-${NAV_LEADER_ID}`}
                    disabled={!initialized()}
                    aria-label={
                      recordingId() === NAV_LEADER_ID
                        ? "Press a key for the leader. Escape cancels."
                        : "Set leader key"
                    }
                    aria-pressed={recordingId() === NAV_LEADER_ID}
                    onClick={() => startRecording(NAV_LEADER_ID)}
                  >
                    <Show
                      when={recordingId() === NAV_LEADER_ID}
                      fallback={
                        <span
                          class="den-keyboard-row__chord"
                          data-testid={`keyboard-chord-${NAV_LEADER_ID}`}
                          data-binding={leaderDisplay()}
                        >
                          <Keycaps
                            chords={[leaderChord()]}
                            platform={platform()}
                          />
                        </span>
                      }
                    >
                      <span
                        class="den-keyboard-bind__listening"
                        data-testid={`keyboard-chord-${NAV_LEADER_ID}`}
                      >
                        Press keys…
                      </span>
                    </Show>
                  </button>
                  <DenButton
                    variant="ghost"
                    compact
                    class="den-keyboard-reset"
                    data-testid={`keyboard-reset-${NAV_LEADER_ID}`}
                    disabled={!initialized() || !hasOverride(NAV_LEADER_ID)}
                    aria-label="Reset leader key"
                    onClick={() => void clearShortcutOverride(NAV_LEADER_ID)}
                  >
                    Reset
                  </DenButton>
                </div>
              </li>
            </ul>
          </section>
        </Show>

        <For each={groups()}>
          {(group) => (
            <section class="den-keyboard-section" data-testid={group.testId}>
              <Show when={group.showHead}>
                <div class="den-keyboard-section__head">
                  <h3 class="den-keyboard-section__title" {...chromeProps()}>{group.label}</h3>
                </div>
              </Show>
              <ul class="den-keyboard-list">
                <For each={group.entries}>
                  {(entry) => {
                    const recording = () => recordingId() === entry.id;
                    const conflict = () => conflictIds().has(entry.id);
                    const custom = () => hasOverride(entry.id);
                    return (
                      <li
                        class="den-keyboard-row"
                        classList={{
                          "den-keyboard-row--recording": recording(),
                          "den-keyboard-row--conflict": conflict(),
                        }}
                        data-testid={`keyboard-row-${entry.id}`}
                        data-command={entry.commandId}
                        data-binding-declaration={entry.id}
                      >
                        <div class="den-keyboard-row__meta">
                          <span class="den-keyboard-row__label">{entry.title}</span>
                          <Show when={entry.provider !== "painted-wolf/platform"}>
                            <span class="den-keyboard-row__provider">
                              {entry.provider}
                            </span>
                          </Show>
                          <Show when={conflict()}>
                            <span
                              class="den-keyboard-row__badge den-keyboard-row__badge--conflict"
                              data-testid={`keyboard-conflict-${entry.id}`}
                              data-tip="Two commands claim this shortcut, so neither one runs. Give one of them a different shortcut."
                            >
                              Conflict
                            </span>
                          </Show>
                          <Show when={custom() && !conflict()}>
                            <span
                              class="den-keyboard-row__badge den-keyboard-row__badge--custom"
                              data-testid={`keyboard-custom-${entry.id}`}
                            >
                              Custom
                            </span>
                          </Show>
                        </div>

                        <div class="den-keyboard-row__actions">
                          <button
                            type="button"
                            class="den-keyboard-bind"
                            classList={{
                              "den-keyboard-bind--recording": recording(),
                              "den-keyboard-bind--conflict": conflict(),
                            }}
                            data-testid={`keyboard-record-${entry.id}`}
                            disabled={!initialized()}
                            aria-label={
                              recording()
                                ? `Press a key for ${entry.title}. Escape cancels.`
                                : `Set shortcut for ${entry.title}`
                            }
                            aria-pressed={recording()}
                            onClick={() => startRecording(entry.id)}
                          >
                            <Show
                              when={recording()}
                              fallback={
                                <span
                                  class="den-keyboard-row__chord"
                                  data-testid={`keyboard-chord-${entry.id}`}
                                  data-binding={bindingFor(entry.id)}
                                >
                                  <Keycaps
                                    chords={chordsFor(entry.id)}
                                    platform={platform()}
                                  />
                                </span>
                              }
                            >
                              <span
                                class="den-keyboard-bind__listening"
                                data-testid={`keyboard-chord-${entry.id}`}
                              >
                                <Show
                                  when={pendingLeader()}
                                  fallback="Press keys…"
                                >
                                  {(leader) =>
                                    `${displayBinding(leader(), platform())} then…`
                                  }
                                </Show>
                              </span>
                            </Show>
                          </button>

                          <DenButton
                            variant="ghost"
                            compact
                            class="den-keyboard-reset"
                            data-testid={`keyboard-reset-${entry.id}`}
                            disabled={!initialized() || !custom()}
                            aria-label={`Reset ${entry.title} to default`}
                            onClick={() => void clearShortcutOverride(entry.id)}
                          >
                            Reset
                          </DenButton>
                        </div>
                      </li>
                    );
                  }}
                </For>
              </ul>
            </section>
          )}
        </For>
      </Show>
      </div>

      <Show when={recordingId()}>
        <p class="den-keyboard-recording-hint" role="status">
          <Show
            when={pendingLeader()}
            fallback={
              <>
                Listening for a key combination. Press the leader key{" "}
                {leaderDisplay()} first to record a two-step sequence. Press
                Escape to cancel.
              </>
            }
          >
            Now press the second key on its own. Press Escape to cancel.
          </Show>
        </p>
      </Show>
    </section>
  );
}
