import type { SessionMode } from "../../api/types.ts";
import { BrowseSegmented, type BrowseSegmentedOption } from "../browse/BrowseSegmented.tsx";
import { SESSION_MODE_DESCRIPTION } from "./session-mode-copy.ts";

type Props = {
  mode: SessionMode;
  started?: boolean;
  disabled?: boolean;
  upgrade?: "sending" | "reconciling";
  onSelect: (mode: SessionMode) => void;
  onSelectionSettled?: (mode: SessionMode) => void;
  ref?: (element: HTMLDivElement) => void;
};

export function SessionModeSwitch(props: Props) {
  const options: BrowseSegmentedOption<SessionMode>[] = [
    {
      id: "agent",
      label: "Agent",
      get description() {
        return props.started ? SESSION_MODE_DESCRIPTION.upgrade : SESSION_MODE_DESCRIPTION.agent;
      },
    },
    {
      id: "ask",
      label: "Ask",
      description: SESSION_MODE_DESCRIPTION.ask,
      get disabled() { return props.started; },
    },
  ];

  return (
    <BrowseSegmented
      ref={props.ref}
      semantics="radio"
      ariaLabel="Session mode"
      class="den-session-mode"
      options={options}
      value={props.mode}
      busy={props.upgrade === "sending"}
      disabled={props.disabled || props.upgrade === "sending"}
      keepFocus
      onChange={props.onSelect}
      onReselect={(mode) => {
        if (!props.started || (mode === "agent" && props.upgrade === "reconciling")) props.onSelect(mode);
      }}
      onSelectionSettled={props.onSelectionSettled}
    />
  );
}
