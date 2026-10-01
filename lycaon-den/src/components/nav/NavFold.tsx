import type { JSX } from "solid-js";

type Props = {
  open: boolean;
  children: JSX.Element;
};

/** Animated height fold for sidebar sections (stays mounted for enter/exit). */
export function NavFold(props: Props) {
  return (
    <div
      class="den-shell-nav-fold"
      classList={{ "den-shell-nav-fold-open": props.open }}
      aria-hidden={!props.open}
      inert={!props.open}
    >
      <div class="den-shell-nav-fold-inner">{props.children}</div>
    </div>
  );
}
