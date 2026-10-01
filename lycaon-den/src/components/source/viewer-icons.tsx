/**
 * The editor's control glyphs — named bindings of a control to its slot and
 * rendered size. The drawing belongs to the slot; see `host-glyphs`.
 */
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

type IconProps = { size?: number };

export function FindIcon(props: IconProps) {
  return <ThemeIcon slot="find" size={props.size} />;
}

export function GotoLineIcon(props: IconProps) {
  return <ThemeIcon slot="goto-line" size={props.size} />;
}

export function CopyIcon(props: IconProps) {
  return <ThemeIcon slot="copy" size={props.size} />;
}

export function CheckIcon(props: IconProps) {
  return <ThemeIcon slot="check" size={props.size} />;
}

export function MoreIcon(props: IconProps) {
  return <ThemeIcon slot="more" size={props.size} />;
}
