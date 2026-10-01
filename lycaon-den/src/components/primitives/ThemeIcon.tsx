/** Renders one host-managed, themeable icon slot. */
import { For, Show } from "solid-js";
import type { ContributionIconNode } from "../../api/types.ts";
import {
  ICON_SLOTS,
  ICON_VIEWBOX,
  type IconSlot,
} from "../../contributions/theme-vocabulary.generated.ts";
import { themedIcon } from "../../contributions/theme-icons.ts";
import { HOST_GLYPHS } from "../../contributions/host-glyphs.tsx";

type Props = {
  slot: IconSlot;
  size?: number;
  class?: string;
  "data-testid"?: string;
};

function IconNode(props: { node: ContributionIconNode }) {
  return (
    <Show
      when={props.node.children?.length}
      fallback={<Shape node={props.node} />}
    >
      <g {...(props.node.attrs ?? {})}>
        <For each={props.node.children}>
          {(child) => <IconNode node={child} />}
        </For>
      </g>
    </Show>
  );
}

function Shape(props: { node: ContributionIconNode }) {
  const attrs = () => props.node.attrs ?? {};
  switch (props.node.tag) {
    case "path":
      return <path {...attrs()} />;
    case "circle":
      return <circle {...attrs()} />;
    case "rect":
      return <rect {...attrs()} />;
    case "ellipse":
      return <ellipse {...attrs()} />;
    case "line":
      return <line {...attrs()} />;
    case "polyline":
      return <polyline {...attrs()} />;
    case "polygon":
      return <polygon {...attrs()} />;
    case "g":
      return <g {...attrs()} />;
  }
}

export function ThemeIcon(props: Props) {
  const frame = () => ICON_SLOTS[props.slot];
  const themed = () => themedIcon(props.slot);
  const filled = () => frame().paint === "fill";
  return (
    <svg
      width={props.size ?? 15}
      height={props.size ?? 15}
      viewBox={ICON_VIEWBOX}
      fill={filled() ? "currentColor" : "none"}
      stroke={filled() ? "none" : "currentColor"}
      // CSS combines the slot weight with the theme scale.
      class={
        props.class ? `den-theme-icon ${props.class}` : "den-theme-icon"
      }
      style={{ "--den-icon-slot-weight": String(frame().weight) }}
      aria-hidden="true"
      data-testid={props["data-testid"]}
    >
      <Show when={themed()} fallback={HOST_GLYPHS[props.slot]()}>
        {(nodes) => (
          <For each={nodes()}>{(node) => <IconNode node={node} />}</For>
        )}
      </Show>
    </svg>
  );
}
