import { Show, createEffect, createSignal, onCleanup } from "solid-js";
import {
  registerFocusRegion,
  releaseFocusRegion,
} from "../shortcuts/focus-region.ts";
import { chromeProps } from "../styling/ui-chrome.ts";
import {
  closeGotoLine,
  gotoLineController,
  submitGotoLine,
} from "./goto-line-controller.ts";

export function GotoLineBar() {
  const [inputEl, setInputEl] = createSignal<HTMLInputElement>();
  const [value, setValue] = createSignal("");
  const [invalid, setInvalid] = createSignal(false);
  const focusClaim = {};

  createEffect(() => {
    const active = gotoLineController.state();
    const input = inputEl();
    if (!active || !input) return;
    setValue(
      String(
        active.target.currentLine,
      ),
    );
    setInvalid(false);
    queueMicrotask(() => {
      input.focus({ preventScroll: true });
      input.select();
    });
  });

  const submit = () => {
    if (submitGotoLine(value())) return;
    setInvalid(true);
    queueMicrotask(() => inputEl()?.focus({ preventScroll: true }));
  };

  return (
    <Show when={gotoLineController.state()} keyed>
      {(active) => (
        <div
          class="den-editor-command-bar den-editor-command-bar--goto"
          role="dialog"
          aria-label="Go to line"
          data-testid="goto-line-bar"
          {...chromeProps()}
          ref={(element) => {
            registerFocusRegion("find", element, focusClaim);
            onCleanup(() => releaseFocusRegion("find", focusClaim));
          }}
        >
          <label class="den-editor-command-bar__label" for="den-goto-line-input">
            Go to line
          </label>
          <input
            id="den-goto-line-input"
            ref={setInputEl}
            class="den-editor-command-bar__input den-input"
            type="text"
            autocomplete="off"
            spellcheck={false}
            value={value()}
            aria-invalid={invalid()}
            aria-describedby={invalid() ? "den-goto-line-error" : undefined}
            data-testid="goto-line-input"
            onInput={(event) => {
              setValue(event.currentTarget.value);
              setInvalid(false);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                event.stopPropagation();
                submit();
              } else if (event.key === "Escape") {
                event.preventDefault();
                event.stopPropagation();
                closeGotoLine();
              }
            }}
          />
          <span class="den-editor-command-bar__count" aria-hidden="true">
            of {active.target.lineCount}
          </span>
          <button
            type="button"
            class="den-editor-command-bar__button den-editor-command-bar__primary"
            data-testid="goto-line-submit"
            onClick={submit}
          >
            Go
          </button>
          <button
            type="button"
            class="den-editor-command-bar__button den-editor-command-bar__close"
            aria-label="Close go to line"
            data-testid="goto-line-close"
            onClick={() => closeGotoLine()}
          >
            ×
          </button>
          <Show when={invalid()}>
            <span
              id="den-goto-line-error"
              class="den-editor-command-bar__error"
              role="status"
            >
              Enter a line number, such as 12 or 12:4.
            </span>
          </Show>
        </div>
      )}
    </Show>
  );
}
