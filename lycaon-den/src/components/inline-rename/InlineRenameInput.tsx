import { createSignal, onMount } from "solid-js";

export type InlineRenameInputProps = {
  initialValue: string;
  class?: string;
  testId?: string;
  maxLength?: number;
  ariaLabel?: string;
  disabled?: boolean;
  spellcheck?: boolean;
  autocomplete?: string;
  /** Select a useful portion of the initial value rather than the whole name. */
  selectionRange?: (value: string) => { start: number; end: number };
  /** Whether blur commits the current draft; false cancels instead. */
  shouldCommitOnBlur?: (value: string) => boolean;
  onCommit: (next: string) => void | Promise<boolean>;
  onCancel: () => void;
};

/**
 * Shared inline rename field: Enter/blur commit, Esc cancel.
 * Empty or unchanged drafts cancel without calling onCommit.
 */
export function InlineRenameInput(props: InlineRenameInputProps) {
  const [draft, setDraft] = createSignal(props.initialValue);
  // The value the draft was seeded from. The name can change under the open editor
  // (an auto-title over SSE); an untouched draft must not commit over it.
  const seeded = props.initialValue.trim();
  let settled = false;
  let field!: HTMLInputElement;

  // `autofocus` applies only to markup present at page load, so a field mounted
  // later focuses itself. Selecting the text makes replacing the name one keystroke.
  onMount(() => {
    field.focus();
    const range = props.selectionRange?.(props.initialValue);
    if (range) {
      field.setSelectionRange(range.start, range.end);
    } else {
      field.select();
    }
  });

  const finish = (mode: "commit" | "cancel") => {
    if (settled) return;
    settled = true;
    if (mode === "cancel") {
      props.onCancel();
      return;
    }
    const next = draft().trim();
    if (!next || next === seeded || next === props.initialValue.trim()) {
      props.onCancel();
      return;
    }
    const result = props.onCommit(next);
    if (result) void result.then(accepted => { if (!accepted) settled = false; }, () => { settled = false; });
  };

  return (
    <input
      ref={field}
      class={props.class}
      data-testid={props.testId}
      value={draft()}
      maxlength={props.maxLength}
      aria-label={props.ariaLabel ?? "Rename"}
      disabled={props.disabled}
      spellcheck={props.spellcheck}
      autocomplete={props.autocomplete}
      onInput={(e) => setDraft(e.currentTarget.value)}
      onBlur={() =>
        finish(props.shouldCommitOnBlur?.(draft()) === false ? "cancel" : "commit")
      }
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          finish("commit");
        }
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          finish("cancel");
        }
      }}
    />
  );
}
