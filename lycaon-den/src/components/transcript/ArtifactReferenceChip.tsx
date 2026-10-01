import type { VisualArtifactSource } from "../../api/types.ts";
import { useArtifactDedup } from "../../chat/visual/artifact-dedup-context.tsx";
import type { ArtifactFace } from "../../chat/visual/visual-artifact-canonical.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { artifactWasDeleted } from "../../chat/visual/artifact-change-store.ts";

type Props = {
  artifactId: string;
  face: ArtifactFace;
  caption?: string | null;
  source?: VisualArtifactSource | null;
};

/** Jumps to this face's full render. */
export function ArtifactReferenceChip(props: Props) {
  const dedup = useArtifactDedup();
  const id = () => props.artifactId.trim();
  const caption = () => {
    const local = props.caption?.trim() ?? "";
    if (local) return local;
    return (
      dedup?.siteFor(id(), props.face)?.caption?.trim() ||
      dedup?.metaFor(id())?.caption?.trim() ||
      "Visual"
    );
  };
  const source = () =>
    props.source ??
    dedup?.siteFor(id(), props.face)?.source ??
    dedup?.metaFor(id())?.source ??
    "render";

  return (
    <button
      type="button"
      class="den-artifact-ref-chip"
      data-testid="artifact-reference-chip"
      data-artifact-id={id()}
      data-artifact-face={props.face}
      aria-label={artifactWasDeleted(id()) ? `${caption()} — deleted` : `Jump to ${caption()}`}
      disabled={artifactWasDeleted(id())}
      onClick={() => dedup?.openCanonical(id(), props.face)}
    >
      <span class="den-artifact-ref-chip__arrow" aria-hidden="true">
        ↗
      </span>
      <span class="den-artifact-ref-chip__caption">{caption()}</span>
      <span class="den-artifact-ref-chip__source">
        {artifactWasDeleted(id()) ? "Deleted" : formatSentenceCase(source())}
      </span>
    </button>
  );
}
