import type { Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { TranscriptDisclosureStore } from "../transcript/presentation/disclosure-state.tsx";
import type { TranscriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import type { TranscriptRevealAnchor } from "../transcript/presentation/transcript-reveal-target.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import type { TranscriptReadingPosition } from "../transcript/layout/transcript-virtualizer.ts";
import type { TranscriptDeliveryKind } from "../transcript/layout/transcript-tail.ts";
export type TranscriptVirtualViewport = {
  /** Samples the transcript origin after outer chrome layout changes. */
  measureOrigin: () => void;
  items: () => readonly TranscriptItem[];
  readingPosition: () => TranscriptReadingPosition | null;
  /** Offset that shows a position again; null while its row is not resident. */
  offsetForPosition: (position: TranscriptReadingPosition) => number | null;
  scrollToIndex: (
    index: number,
    opts?: { align?: "start" | "center" | "end" | "auto" },
  ) => void;
  scrollToOffset: (offset: number, opts: { glide: boolean }) => void;
  ensureAnchorLoaded: (anchor: TranscriptRevealAnchor) => Promise<void>;
  /** Loads older history until the row is resident or history ends. */
  ensureRowLoaded: (rowKey: string) => Promise<void>;
  /** Epoch ms dating the reader's day once its day label is above them; null otherwise. */
  readingDay: () => number | null;
};

export type TranscriptViewportRuntime = {
  client: () => LycaonClient | undefined;
  appStore: AppStore;
  sessionId: () => string | undefined;
  tabOpen: () => boolean;
  panelRetracted: () => boolean;
  panelHeightPx: () => number;
  onPanelRetractedChange: (retracted: boolean) => void;
};

export type TranscriptContentChange =
  | {
    delivery: Extract<TranscriptDeliveryKind, "prose">;
    rowKey: string;
    firstContent: boolean;
  }
  | { delivery: Extract<TranscriptDeliveryKind, "structural"> };

export type TranscriptViewportController = {
  sessionId: () => string;
  /** The viewport keeps the latest message in view. */
  following: Accessor<boolean>;
  /** False while presented history ends at a gap before the live tail. */
  presentsLiveTail: Accessor<boolean>;
  stream: () => HTMLElement | null;
  disclosures: TranscriptDisclosureStore;
  attachStream: (stream: HTMLElement | null) => void;
  /** Returns whether a saved reading position was restored. */
  activateSession: (
    sessionId: string,
    previousSessionId?: string,
    preserveCurrent?: boolean,
  ) => boolean;
  saveSession: (sessionId?: string) => void;
  bindRuntime: (runtime: TranscriptViewportRuntime) => () => void;
  attachVirtualWindow: (view: TranscriptVirtualViewport) => () => void;
  /** The day the reader is in, once its label has scrolled above them. */
  readingDay: () => number | null;
  /** Virtual rows changed; a saved position waiting for its row retries. */
  rowsChanged: () => void;
  commitReveal: (offset: number, glide: boolean) => void;
  /** Carries the offset with content that moved above the reading row; returns the applied shift. */
  shiftVirtualContent: (deltaY: number, fromOffset: number) => number;
  /** Reader input preserves the current reading position. */
  stopFollowing: () => void;
  /**
   * Holds reader-driven disclosure expansion or collapse until final geometry is available.
   * A collapse also lets the native range contract without changing follow.
   */
  beginDisclosureMotion: (
    key: TranscriptDisclosureKey | undefined,
    direction: "open" | "close",
  ) => () => void;
  resumeFollowing: () => void;
  jumpToTail: (smooth?: boolean) => void;
  primeTail: (fadeMs?: number) => void;
  contentChanged: (change: TranscriptContentChange) => void;
  chromeChanged: (opts: {
    tabOpen: boolean;
    panelRetracted: boolean;
    panelHeightPx: number;
  }) => void;
  revealAnchor: (
    anchor: TranscriptRevealAnchor,
    opts?: {
      align?: "start" | "center" | "end" | "auto";
      signal?: AbortSignal;
      element?: () => HTMLElement | null;
    },
  ) => Promise<boolean>;
  setNavigationDisclosures: (keys: readonly TranscriptDisclosureKey[]) => void;
  clearNavigationDisclosures: () => void;
  ensureVisible: (
    element: HTMLElement,
    opts?: { align?: "start" | "center" | "end" | "nearest"; smooth?: boolean },
  ) => void;
};
