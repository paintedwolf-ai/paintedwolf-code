import { ChromeDragSurface } from "../shell/ChromeDragSurface.tsx";
import { liveResolvedKeymap } from "../../contributions/frame-keymap.ts";
import { For, Show, createMemo, createSignal } from "solid-js";
import {
  COMMAND_SCOPE_LABEL,
  COMMAND_SCOPE_ORDER,
  commandDisplaySections,
  shortcutDisplayEntries,
  type ShortcutDisplayEntry,
} from "../../shortcuts/command-display.ts";
import {
  nativeCommandId,
} from "../../contributions/dispatch.ts";
import { contributionFrame } from "../../contributions/contribution-store.ts";
import { formatChordsDisplay } from "../../shortcuts/display-binding-for.ts";
import { shortcutPlatform } from "../../shortcuts/platform.ts";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { createOverlayScopeFocusTrap, shellChromeInertTargets } from "../../platform/interaction/modal-focus-trap.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { Keycaps } from "./Keycaps.tsx";

export type ShortcutHelpOverlayProps = {
  open: boolean;
  onClose: () => void;
  /** Navigate to Settings → General → Keyboard for editing. */
  onOpenKeyboardSettings?: () => void;
};

/** Read-only cheat sheet from the live resolved keymap. */
export function ShortcutHelpOverlay(props: ShortcutHelpOverlayProps) {
  const platform = () => shortcutPlatform();
  const [dialogEl, setDialogEl] = createSignal<HTMLDivElement | undefined>();

  createOverlayScopeFocusTrap(
    () => props.open,
    dialogEl,
    {
      inertTarget: () => shellChromeInertTargets(),
    },
  );

  const resolved = createMemo(() =>
    liveResolvedKeymap(platform(), shortcutOverrides()),
  );

  // Groups such as Navigation recur across scopes, so each renders beneath its scope heading.
  const scopes = createMemo(() => {
    const sections = commandDisplaySections(
      shortcutDisplayEntries(contributionFrame()),
    );
    return COMMAND_SCOPE_ORDER.map((scope) => {
      const inScope = sections.filter((section) => section.scope === scope);
      return {
        scope,
        label: COMMAND_SCOPE_LABEL[scope],
        ungrouped: inScope.filter((section) => !section.group),
        groups: inScope.filter((section) => section.group),
      };
    }).filter(
      (block) => block.ungrouped.length > 0 || block.groups.length > 0,
    );
  });

  const chordsFor = (id: string) => resolved().byDeclaration.get(id) ?? [];
  const commandChordsFor = (id: string | null) => id ? resolved().byCommand.get(id) ?? [] : [];

  const bindingFor = (id: string) =>
    formatChordsDisplay(chordsFor(id), platform());

  const entryList = (entries: readonly ShortcutDisplayEntry[]) => (
    <ul class="den-keyboard-list">
      <For each={entries}>
        {(entry) => (
          <li
            class="den-keyboard-row den-shortcut-help__row"
            data-testid={`shortcut-help-row-${entry.id}`}
            data-command={entry.commandId}
            data-binding-declaration={entry.id}
          >
            <span class="den-keyboard-row__meta">
              <span class="den-keyboard-row__label">{entry.title}</span>
              <Show when={entry.provider !== "painted-wolf/platform"}>
                <span class="den-keyboard-row__provider">{entry.provider}</span>
              </Show>
            </span>
            <span
              class="den-keyboard-row__chord"
              data-testid={`shortcut-help-chord-${entry.id}`}
              data-binding={bindingFor(entry.id)}
            >
              <Keycaps chords={chordsFor(entry.id)} platform={platform()} />
            </span>
          </li>
        )}
      </For>
    </ul>
  );

  return (
    <Show when={props.open}>
      <div
        class="den-dialog-backdrop den-shortcut-help-backdrop"
        data-testid="shortcut-help-backdrop"
        onClick={() => props.onClose()}
      >
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <div
          class="den-shortcut-help"
          role="dialog"
          aria-modal="true"
          aria-labelledby="shortcut-help-title"
          data-testid="shortcut-help-overlay"
          ref={setDialogEl}
          onClick={(e) => e.stopPropagation()}
        >
          <header class="den-shortcut-help__header" {...chromeProps()}>
            <h2 id="shortcut-help-title" class="den-shortcut-help__title">
              Keyboard shortcuts
            </h2>
            <ChromeCloseButton
              class="den-dialog__close"
              label="Close"
              testId="shortcut-help-close"
              onClick={() => props.onClose()}
            />
          </header>

          <Scrollport class="den-shortcut-help__body" contentClass="den-shortcut-help__body-content">
            <For each={scopes()}>
              {(block) => (
                <section
                  class="den-shortcut-help__scope-block"
                  data-testid={`shortcut-help-scope-${block.scope}`}
                >
                  <h3 class="den-shortcut-help__scope">{block.label}</h3>
                  <For each={block.ungrouped}>
                    {(section) => entryList(section.entries)}
                  </For>
                  <For each={block.groups}>
                    {(section) => (
                      <section
                        class="den-shortcut-help__group"
                        data-testid={`shortcut-help-group-${section.id}`}
                      >
                        <h4 class="den-shortcut-help__group-title">
                          {section.label}
                        </h4>
                        {entryList(section.entries)}
                      </section>
                    )}
                  </For>
                </section>
              )}
            </For>
          </Scrollport>

          <footer class="den-shortcut-help__footer">
            <Show when={props.onOpenKeyboardSettings}>
              <DenButton
                variant="ghost"
                compact
                data-testid="shortcut-help-open-settings"
                onClick={() => props.onOpenKeyboardSettings?.()}
              >
                Edit shortcuts
              </DenButton>
            </Show>
            <p class="den-shortcut-help__footnote">
              Press{" "}
              <span class="den-shortcut-help__footnote-keys">
                <Keycaps
                  chords={commandChordsFor(nativeCommandId("overlay.dismiss"))}
                  platform={platform()}
                />
              </span>{" "}
              to close
            </p>
          </footer>
        </div>
      </div>
    </Show>
  );
}
