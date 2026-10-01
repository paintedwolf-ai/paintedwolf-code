import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

/**
 * Controls governed by the switch just above. They stay visible so the link
 * reads; while the switch is off they dim and every control inside is locked.
 */
export function SettingsGovernedGroup(props: {
  /** Names the group for assistive tech. */
  label: string;
  active: boolean;
  /** Indents under the switch; off when the group is a whole panel section. */
  inset?: boolean;
  class?: string;
  testId?: string;
  children: JSX.Element;
}) {
  return (
    <fieldset
      class={cn("den-settings-governed", props.class)}
      data-inset={props.inset === false ? undefined : "true"}
      disabled={!props.active}
      aria-label={props.label}
      data-testid={props.testId}
    >
      {props.children}
    </fieldset>
  );
}
