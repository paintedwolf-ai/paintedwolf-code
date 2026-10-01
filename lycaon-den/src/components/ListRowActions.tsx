/** Inline list-row controls — hover-revealed ghost icon buttons. */

import { ThemeIcon } from "./primitives/ThemeIcon.tsx";

type RenameProps = {
  label: string;
  testId?: string;
  onRename: () => void;
};

type PinProps = {
  pinned: boolean;
  testId?: string;
  onToggle: () => void;
};

/** Pin or unpin a row; use inside `.den-list-row__actions`. */
export function RowPinButton(props: PinProps) {
  return (
    <button
      type="button"
      class="den-row-action den-inset-icon-btn"
      data-testid={props.testId}
      aria-label={props.pinned ? "Unpin chat" : "Pin chat"}
      aria-pressed={props.pinned}
      onClick={(e) => {
        e.stopPropagation();
        props.onToggle();
      }}
    >
      <ThemeIcon slot="pin" size={14} />
    </button>
  );
}

/** Start an inline rename; use inside `.den-list-row__actions`. */
export function RowRenameButton(props: RenameProps) {
  return (
    <button
      type="button"
      class="den-row-action den-inset-icon-btn"
      data-testid={props.testId}
      aria-label={props.label}
      onClick={(e) => {
        e.stopPropagation();
        props.onRename();
      }}
    >
      <ThemeIcon slot="edit" size={14} />
    </button>
  );
}
