import {
  createContext,
  useContext,
  type Accessor,
  type ParentProps,
} from "solid-js";
import { useTranscriptViewport } from "../stream/transcript-viewport.tsx";
import {
  artifactFaceFromEntryKey,
  faceSiteKey,
  isCanonicalArtifactEntry,
  type ArtifactFace,
  type CanonicalArtifactPlacement,
  type CanonicalArtifactPlacements,
  type VisualArtifactMeta,
} from "./visual-artifact-canonical.ts";

type Opener = () => void;

export type ArtifactDedupApi = {
  isCanonicalEntry: (artifactId: string, entryKey: string) => boolean;
  siteFor: (
    artifactId: string,
    kind: ArtifactFace,
  ) => CanonicalArtifactPlacement | undefined;
  metaFor: (artifactId: string) => VisualArtifactMeta | undefined;
  openCanonical: (artifactId: string, kind: ArtifactFace) => void;
  registerCanonical: (
    artifactId: string,
    entryKey: string,
    open: Opener,
  ) => () => void;
};

const ArtifactDedupContext = createContext<ArtifactDedupApi>();

/** Per-face placement and lightbox openers for one transcript, over placements the transcript resolves once. */
export function ArtifactDedupProvider(
  props: ParentProps<{
    placements: Accessor<CanonicalArtifactPlacements>;
    revealCanonical?: (placement: CanonicalArtifactPlacement) => boolean;
  }>,
) {
  const viewport = useTranscriptViewport();
  const placements = props.placements;
  const openers = new Map<string, Opener>();
  const pendingOpen = new Set<string>();

  const scrollCanonicalIntoView = (entryKey: string) => {
    const escaped = entryKey.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
    const element = document.querySelector<HTMLElement>(
      `[data-artifact-canonical="${escaped}"]`,
    );
    if (element) viewport?.ensureVisible(element, { align: "center" });
  };

  const openMountedCanonical = (kind: ArtifactFace, artifactId: string): boolean => {
    const open = openers.get(faceSiteKey(kind, artifactId));
    if (!open) return false;
    const site = placements().site(kind, artifactId);
    if (site) scrollCanonicalIntoView(site.entryKey);
    open();
    return true;
  };

  const api: ArtifactDedupApi = {
    isCanonicalEntry: (artifactId, entryKey) =>
      isCanonicalArtifactEntry(placements(), artifactId, entryKey),
    siteFor: (artifactId, kind) => placements().site(kind, artifactId),
    metaFor: (artifactId) => placements().meta(artifactId),
    openCanonical: (artifactId, kind) => {
      const id = artifactId.trim();
      if (!id) return;
      if (openMountedCanonical(kind, id)) return;
      const site = placements().site(kind, id);
      if (!site || !props.revealCanonical) return;
      pendingOpen.add(faceSiteKey(kind, id));
      if (!props.revealCanonical(site)) pendingOpen.delete(faceSiteKey(kind, id));
    },
    registerCanonical: (artifactId, entryKey, open) => {
      const id = artifactId.trim();
      const key = entryKey.trim();
      if (!id || !key) return () => {};
      if (!isCanonicalArtifactEntry(placements(), id, key)) return () => {};
      const kind = artifactFaceFromEntryKey(key, id);
      if (!kind) return () => {};
      const openerKey = faceSiteKey(kind, id);
      openers.set(openerKey, open);
      if (pendingOpen.delete(openerKey)) {
        queueMicrotask(() => {
          if (openers.get(openerKey) !== open) return;
          scrollCanonicalIntoView(key);
          open();
        });
      }
      return () => {
        if (openers.get(openerKey) === open) {
          openers.delete(openerKey);
        }
      };
    },
  };

  return (
    <ArtifactDedupContext.Provider value={api}>
      {props.children}
    </ArtifactDedupContext.Provider>
  );
}

export function useArtifactDedup(): ArtifactDedupApi | undefined {
  return useContext(ArtifactDedupContext);
}
