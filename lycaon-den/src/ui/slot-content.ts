import type { JSX } from "solid-js";

/** An optional fixed-position surface with lazily created content. */
export type SlotContent = {
  present: () => boolean;
  children: () => JSX.Element;
};
