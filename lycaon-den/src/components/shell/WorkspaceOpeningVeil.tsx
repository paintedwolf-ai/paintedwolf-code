import type { JSX } from "solid-js";

type Props = {
  visible: boolean;
  children: JSX.Element;
};

/** Covers the entire workspace until its rail and columns can paint together. */
export function WorkspaceOpeningVeil(props: Props) {
  return (
    <div
      class="den-shell-workspace-veil"
      classList={{
        "den-shell-workspace-veil--hidden": !props.visible,
      }}
      data-testid="shell-workspace-veil"
      aria-hidden={props.visible ? undefined : true}
      inert={props.visible ? undefined : true}
    >
      {props.children}
    </div>
  );
}
