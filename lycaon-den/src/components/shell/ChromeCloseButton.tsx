import { chromeProps } from "../../styling/ui-chrome.ts";

/** Dismiss controls inherit non-selectable chrome. */
export function ChromeCloseButton(props: {
  class: string;
  label: string;
  onClick: () => void;
  testId?: string;
}) {
  return (
    <button
      type="button"
      class={props.class}
      aria-label={props.label}
      data-testid={props.testId}
      {...chromeProps()}
      onClick={() => props.onClick()}
    >
      ×
    </button>
  );
}
