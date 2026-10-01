import { twMerge } from "./tw-merge.ts";

/** Join class names with Tailwind conflict-merge; falsy values skipped. */
export function cn(...parts: Array<string | false | null | undefined>): string {
  return twMerge(parts.filter(Boolean).join(" "));
}
