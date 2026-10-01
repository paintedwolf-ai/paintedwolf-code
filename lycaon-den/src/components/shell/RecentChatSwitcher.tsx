import { For, Show, createEffect, createSignal, onCleanup } from "solid-js";
import { ATTENTION_CLASS_LABEL } from "../../attention/attention-model.ts";
import {
  initialMruIndex,
  nextMruIndex,
  type MruEntry,
} from "../../chat/session/mru-switcher-model.ts";

export type RecentChatSwitcherProps = {
  /** Most-recently-visited first; index 0 is the chat currently in front. */
  entries: readonly MruEntry[];
  onCommit: (entry: MruEntry) => void;
};

export type RecentChatSwitcherHandle = {
  /** Open on the neighbouring entry, or step if already open. */
  step: (direction: 1 | -1) => void;
  /** True while the gesture is on screen. */
  isOpen: () => boolean;
  /** Dismiss without navigating. */
  cancel: () => void;
};

/** Hold-to-cycle switcher for recent chats. */
export function RecentChatSwitcher(
  props: RecentChatSwitcherProps & { ref?: (handle: RecentChatSwitcherHandle) => void },
) {
  const [open, setOpen] = createSignal(false);
  const [index, setIndex] = createSignal(0);

  const close = () => {
    setOpen(false);
    setIndex(0);
  };

  const commit = () => {
    if (!open()) return;
    // Capture the selected index before reset.
    const at = index();
    const entry = props.entries[at];
    close();
    // The current chat is a no-op.
    if (entry && at !== 0) props.onCommit(entry);
  };

  props.ref?.({
    step: (direction) => {
      const count = props.entries.length;
      if (count === 0) return;
      if (!open()) {
        setIndex(initialMruIndex(count, direction));
        setOpen(true);
        return;
      }
      setIndex((i) => nextMruIndex(i, count, direction));
    },
    isOpen: open,
    cancel: close,
  });

  createEffect(() => {
    if (!open()) return;
    const onKeyUp = (e: KeyboardEvent) => {
      // Releasing the opening modifier commits the selection.
      if (e.key === "Control" || e.key === "Meta") commit();
    };
    window.addEventListener("keyup", onKeyUp);
    // Blur cancels gestures that cannot receive keyup.
    window.addEventListener("blur", close);
    onCleanup(() => {
      window.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", close);
    });
  });

  const liveSelection = () => {
    if (!open()) return "";
    const entry = props.entries[index()];
    if (!entry) return "";
    return `Recent chat ${index() + 1} of ${props.entries.length}: ${entry.title}`;
  };

  return (
    <>
      <span
        class="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        data-testid="recent-switcher-status"
      >
        {liveSelection()}
      </span>
      <Show when={open() && props.entries.length > 0}>
        <div class="recent-switcher-backdrop" data-testid="recent-switcher-backdrop">
          <div
            class="recent-switcher"
            role="dialog"
            aria-label="Recent chats"
            data-testid="recent-switcher"
          >
            <ul class="recent-switcher__list">
              <For each={props.entries}>
                {(entry, i) => (
                  <li>
                    <button
                      type="button"
                      class="recent-switcher__row"
                      classList={{
                        "recent-switcher__row--active": i() === index(),
                      }}
                      data-testid="recent-switcher-row"
                      data-active={i() === index() ? "true" : undefined}
                      data-session-id={entry.sessionId}
                      aria-current={i() === index() ? "true" : undefined}
                      onMouseEnter={() => setIndex(i())}
                      onClick={() => {
                        setIndex(i());
                        commit();
                      }}
                    >
                      <Show when={entry.attention}>
                        {(att) => (
                          <span
                            class="den-attention-chip__dot recent-switcher__dot"
                            data-attention-class={att().class}
                            aria-label={ATTENTION_CLASS_LABEL[att().class]}
                          />
                        )}
                      </Show>
                      <span class="recent-switcher__primary">{entry.title}</span>
                      <Show when={entry.projectName}>
                        {(name) => (
                          <span class="recent-switcher__project">{name()}</span>
                        )}
                      </Show>
                    </button>
                  </li>
                )}
              </For>
            </ul>
            <p class="recent-switcher__hint">
              Hold and press again · release to open
            </p>
          </div>
        </div>
      </Show>
    </>
  );
}
