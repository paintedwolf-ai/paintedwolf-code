import { EditorSelection } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import type { LycaonClient } from "../../api/client.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { ManagedSecret, SecretMarkPreview } from "../../api/types.ts";
import {
  secretSpanAt,
  siblingSecretSpans,
  type SecretSpanMark,
} from "../../components/source/secrets/secret-span-model.ts";
import { getSecretSpanMarks } from "../../components/source/secrets/secret-span-decorations.ts";
import { MarkSecretFailure } from "../../components/source/secrets/mark-secret-failure.ts";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import type { SyncedEditorDocument } from "../documents/editor-document.ts";

/** Identifies a host-synchronized document. */
export type SecretActionTarget = {
  projectId: string;
  documentId: string;
  view: EditorView;
  /** Publishes editor bytes and returns the host revision. */
  sync: () => Promise<SyncedEditorDocument | null>;
};

/** Publishes the draft and verifies the host text. */
async function syncedDocument(
  target: SecretActionTarget,
): Promise<SyncedEditorDocument> {
  const synced = await target.sync().catch(() => null);
  if (!synced || synced.text !== target.view.state.doc.toString()) {
    throw new MarkSecretFailure(SECRET_SPAN_COPY.unsyncedDraft);
  }
  return synced;
}

export function runeRange(text: string, from: number, to: number): {
  start: number;
  end: number;
} {
  return {
    start: Array.from(text.slice(0, from)).length,
    end: Array.from(text.slice(0, to)).length,
  };
}

/** The range a secret action addresses, given where a right-click landed. */
export function secretActionRange(view: EditorView, pointer?: number | null): {
  from: number;
  to: number;
  span: SecretSpanMark | null;
} | null {
  const main = view.state.selection.main;
  const marks = getSecretSpanMarks(view);
  // A right-click leaves the caret behind, so a pointer outside the selection
  // addresses the span it landed in rather than whatever the caret sits on.
  const insideSelection =
    !main.empty && pointer != null && pointer >= main.from && pointer <= main.to;
  if (pointer != null && !insideSelection) {
    const pointed = secretSpanAt(marks, pointer);
    if (pointed) return { from: pointed.from, to: pointed.to, span: pointed };
  }
  if (!main.empty) {
    const covering = marks.find(
      (mark) => mark.from <= main.from && mark.to >= main.to,
    );
    return { from: main.from, to: main.to, span: covering ?? null };
  }
  const span = secretSpanAt(marks, main.head);
  return span ? { from: span.from, to: span.to, span } : null;
}

/** The host revision and rune range a mark sheet was opened against. */
type AnchoredRange = { revision: number; start: number; end: number };

/**
 * A selection pinned to the host revision it was made against. The sheet
 * previews and marks that one range, so bytes that move under an open sheet
 * are refused by the host as a stale revision rather than protected by offset.
 */
export type AnchoredSecretSelection = {
  preview: (client: LycaonClient, trim: boolean) => Promise<SecretMarkPreview>;
  mark: (
    client: LycaonClient,
    args: { name: string; purpose: string; trim: boolean; operationId: string },
  ) => Promise<ManagedSecret>;
};

export function anchorSecretSelection(
  target: SecretActionTarget,
  from: number,
  to: number,
): AnchoredSecretSelection {
  let anchored: Promise<AnchoredRange> | null = null;
  // The draft is published once; a publish that failed is retried next time.
  const anchor = (): Promise<AnchoredRange> => {
    anchored ??= syncedDocument(target).then(
      (synced) => ({ revision: synced.revision, ...runeRange(synced.text, from, to) }),
      (error: unknown) => {
        anchored = null;
        throw error;
      },
    );
    return anchored;
  };
  return {
    preview: async (client, trim) => {
      const range = await anchor();
      return staleAsFailure(
        client.previewEditorSecretMark(target.projectId, target.documentId, { ...range, trim }),
      );
    },
    mark: async (client, args) => {
      const range = await anchor();
      return staleAsFailure(
        client.markEditorSecret(target.projectId, target.documentId, {
          range: { ...range, trim: args.trim },
          name: args.name,
          purpose: args.purpose,
          operation_id: args.operationId,
        }),
      );
    },
  };
}

/** A revision the host no longer holds means the file moved under the sheet. */
async function staleAsFailure<T>(call: Promise<T>): Promise<T> {
  try {
    return await call;
  } catch (error) {
    if (error instanceof LycaonApiError && error.code === "editor_revision_conflict") {
      throw new MarkSecretFailure(SECRET_SPAN_COPY.staleDocument);
    }
    throw error;
  }
}

export function selectSiblingSpans(view: EditorView, span: SecretSpanMark): number {
  const siblings = siblingSecretSpans(getSecretSpanMarks(view), span);
  if (siblings.length === 0) return 0;
  const ranges = [span, ...siblings].map((mark) =>
    EditorSelection.range(mark.from, mark.to),
  );
  view.dispatch({
    selection: EditorSelection.create(ranges, ranges.length - 1),
    scrollIntoView: true,
  });
  return siblings.length;
}
