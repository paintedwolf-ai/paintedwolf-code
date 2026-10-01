import { extendTailwindMerge } from "tailwind-merge";

/** Conflict-merge tuned for Den @utility recipes. */
export const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      btn: [
        "btn-primary",
        "btn-secondary",
        "btn-ghost",
        "btn-link",
        "btn-icon",
        "btn-danger",
      ],
      "den-control": ["den-input", "den-select", "den-rules-input"],
    },
  },
  // Den @utility class names are outside Tailwind's default class-group typings.
} as Parameters<typeof extendTailwindMerge>[0]);
