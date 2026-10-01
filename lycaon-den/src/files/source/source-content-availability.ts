import { formatSourceBytes } from "../../components/source/editor/source-editor-model.ts";
import type { SourceComparison } from "../../api/types.ts";

type SourceContentAvailability = NonNullable<
  SourceComparison["after"]
>["availability"];

/** One comparison side as the host reports it. */
export type ContentSide = {
  availability: SourceContentAvailability;
  reason?: string | null;
  sizeBytes?: number | null;
};

/**
 * Why a side's text is not shown. Expected boundaries (size, binary, directory)
 * are `info`; a side the host could not produce is an `error`.
 */
export type ContentNotice = {
  tone: "info" | "error";
  message: string;
};

export function sourceContentNotice(side: ContentSide): ContentNotice | null {
  switch (side.availability) {
    case "available":
    case "absent":
      return null;
    case "not_captured":
      if (side.reason === "content_too_large") {
        return {
          tone: "info",
          message: side.sizeBytes == null
            ? "This version was too large to keep."
            : `This version was too large to keep (${formatSourceBytes(side.sizeBytes)}).`,
        };
      }
      return { tone: "info", message: "Content was not captured for this version." };
    case "binary":
      return { tone: "info", message: "This version is binary and can’t be shown as text." };
    case "directory":
      return { tone: "info", message: "This version is a directory." };
    case "unresolved":
      return { tone: "error", message: "Content for this version is unresolved." };
    case "unavailable":
      return { tone: "error", message: "Content for this version is unavailable." };
  }
  return { tone: "error", message: "Content for this version is unavailable." };
}

export function sourceContentIsReadable(
  availability: SourceContentAvailability,
): boolean {
  return sourceContentNotice({ availability }) == null;
}
