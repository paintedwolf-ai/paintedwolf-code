import { ThemeIcon } from "./ThemeIcon.tsx";
/**
 * Pushpin glyph shared by every pinned-chat affordance (sidebar row, All chats
 * table).
 */
export function PinIcon(props: { size?: number }) {
  return <ThemeIcon slot="pin" size={props.size ?? 11} />;
}
