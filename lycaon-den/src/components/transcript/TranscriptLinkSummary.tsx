import type { JSX } from "solid-js";
import {
  TranscriptDisclosureToggle,
  type TranscriptDisclosure,
} from "../../chat/transcript/presentation/transcript-disclosure.tsx";

type Props = {
  disclosure: TranscriptDisclosure;
  label: string;
  labelTestId?: string;
  /** Marks a label the reader should check before relying on it. */
  attention?: boolean;
  onClick?: JSX.EventHandler<HTMLButtonElement, MouseEvent>;
  "aria-label"?: string;
  /** Facts shown after the label. */
  children?: JSX.Element;
};

/** The header of a link disclosure: its label, any facts, and the chevron last. */
export function TranscriptLinkSummary(props: Props) {
  return (
    <TranscriptDisclosureToggle
      disclosure={props.disclosure}
      class="den-transcript-link-summary"
      onClick={props.onClick}
      aria-label={props["aria-label"]}
    >
      <span
        class="den-transcript-link-summary-label"
        data-testid={props.labelTestId}
        data-attention={props.attention ? "" : undefined}
      >
        {props.label}
      </span>
      {props.children}
      <span class="den-disclosure-chevron" aria-hidden="true" />
    </TranscriptDisclosureToggle>
  );
}
