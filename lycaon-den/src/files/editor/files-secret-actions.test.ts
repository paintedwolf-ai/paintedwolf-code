// @vitest-environment jsdom
import { stubClient } from "../../test/client-fixture.ts";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { SecretScreen } from "../../api/types.ts";
import {
  secretSpanExtension,
  setSecretScreen,
} from "../../components/source/secrets/secret-span-decorations.ts";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import {
  anchorSecretSelection,
  runeRange,
  secretActionRange,
  selectSiblingSpans,
} from "./files-secret-actions.ts";

function viewWith(doc: string, screen?: SecretScreen): EditorView {
  const view = new EditorView({
    // The Files editor allows multiple selections; sibling selection depends on it.
    state: EditorState.create({
      doc,
      extensions: [
        EditorState.allowMultipleSelections.of(true),
        secretSpanExtension,
      ],
    }),
  });
  if (screen) setSecretScreen(view, screen);
  return view;
}

/** A target whose publish lands exactly what the editor shows. */
const target = (view: EditorView, revision = 41) => ({
  projectId: "p1",
  documentId: "d1",
  view,
  sync: vi.fn(async () => ({ revision, text: view.state.doc.toString() })),
});

describe("runeRange", () => {
  it("converts UTF-16 positions to the rune offsets the host counts in", () => {
    const text = 'const a = "🔑"; const k = "AKIAQYJK5TXV4NZR7SGB";';
    const from = text.indexOf("AKIA");
    const range = runeRange(text, from, from + 20);
    // Two UTF-16 units for the emoji collapse to one rune.
    expect(range.start).toBe(from - 1);
    expect(range.end).toBe(range.start + 20);
  });

  it("agrees with positions when the document is all BMP", () => {
    expect(runeRange("plain ascii only", 6, 11)).toEqual({ start: 6, end: 11 });
  });
});

describe("secretActionRange", () => {
  const screen: SecretScreen = {
    truncated: false,
    spans: [{ start: 5, end: 12, state: "detected", rule_id: "aws" }],
  };

  it("uses the caret's span when nothing is selected", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 7 } });
    const range = secretActionRange(view);
    expect(range).toMatchObject({ from: 5, to: 12 });
    expect(range?.span?.state).toBe("detected");
    view.destroy();
  });

  it("returns nothing when the caret sits outside every span", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 17 } });
    expect(secretActionRange(view)).toBeNull();
    view.destroy();
  });

  it("prefers an explicit selection over the span under the caret", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 15, head: 19 } });
    const range = secretActionRange(view);
    expect(range).toMatchObject({ from: 15, to: 19, span: null });
    view.destroy();
  });

  it("reports the covering span when the selection sits inside one", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 6, head: 10 } });
    expect(secretActionRange(view)?.span?.state).toBe("detected");
    view.destroy();
  });

  it("uses the span under the pointer when the caret sits elsewhere", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 17 } });
    const range = secretActionRange(view, 8);
    expect(range).toMatchObject({ from: 5, to: 12 });
    expect(range?.span?.state).toBe("detected");
    view.destroy();
  });

  it("takes the span at either edge of the pointed-at span", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 0 } });
    expect(secretActionRange(view, 5)).toMatchObject({ from: 5, to: 12 });
    expect(secretActionRange(view, 12)).toMatchObject({ from: 5, to: 12 });
    view.destroy();
  });

  it("keeps the selection when the pointer lands inside it", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 6, head: 19 } });
    expect(secretActionRange(view, 8)).toMatchObject({ from: 6, to: 19, span: null });
    view.destroy();
  });

  it("addresses the pointed-at span over a selection made elsewhere", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 15, head: 19 } });
    expect(secretActionRange(view, 8)).toMatchObject({ from: 5, to: 12 });
    view.destroy();
  });

  it("falls back to the selection when the pointer is on no span", () => {
    const view = viewWith("word secretval tail", screen);
    view.dispatch({ selection: { anchor: 15, head: 19 } });
    expect(secretActionRange(view, 0)).toMatchObject({ from: 15, to: 19, span: null });
    view.destroy();
  });
});

describe("selectSiblingSpans", () => {
  it("selects every other occurrence of the same value in this buffer", () => {
    const view = viewWith("aaaa bbbb aaaa", {
      truncated: false,
      spans: [
        { start: 0, end: 4, state: "detected", rule_id: "x", shape: "aaaa" },
        { start: 10, end: 14, state: "detected", rule_id: "x", shape: "aaaa" },
      ],
    });
    view.dispatch({ selection: { anchor: 1 } });
    const range = secretActionRange(view)!;
    expect(selectSiblingSpans(view, range.span!)).toBe(1);
    expect(view.state.selection.ranges).toHaveLength(2);
    view.destroy();
  });

  it("does nothing when a value appears once", () => {
    const view = viewWith("aaaa bbbb", {
      truncated: false,
      spans: [{ start: 0, end: 4, state: "detected", rule_id: "x", shape: "aaaa" }],
    });
    view.dispatch({ selection: { anchor: 1 } });
    const range = secretActionRange(view)!;
    expect(selectSiblingSpans(view, range.span!)).toBe(0);
    expect(view.state.selection.ranges).toHaveLength(1);
    view.destroy();
  });
});

function clientSpies() {
  const previewEditorSecretMark = vi.fn(async () => ({
    eligible: true,
    start: 7,
    end: 27,
    rune_length: 20,
    byte_length: 20,
  }));
  const markEditorSecret = vi.fn(async (
    _projectId: string,
    _documentId: string,
    _req: unknown,
  ) => ({
    reference: "{{paintedwolf-secret:x}}",
    name: "Key",
    scope: "project" as const,
    origin: "file_marked" as const,
    created_at: "now",
    state: "active" as const,
  }));
  return {
    previewEditorSecretMark,
    markEditorSecret,
    client: stubClient({
      previewEditorSecretMark,
      markEditorSecret,
    }),
  };
}

const markArgs = {
  name: "Key",
  purpose: "Authenticate project services",
  trim: true,
  operationId: "op-1",
};

describe("anchorSecretSelection", () => {
  it("publishes the draft once and previews and marks that one revision", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    const t = target(view, 42);
    const selection = anchorSecretSelection(t, 7, 27);

    await selection.preview(spies.client, true);
    await selection.preview(spies.client, false);
    await selection.mark(spies.client, markArgs);

    expect(t.sync).toHaveBeenCalledTimes(1);
    expect(spies.previewEditorSecretMark).toHaveBeenNthCalledWith(1, "p1", "d1", {
      revision: 42, start: 7, end: 27, trim: true,
    });
    expect(spies.previewEditorSecretMark).toHaveBeenNthCalledWith(2, "p1", "d1", {
      revision: 42, start: 7, end: 27, trim: false,
    });
    expect(spies.markEditorSecret.mock.calls[0]![2]).toMatchObject({
      range: { revision: 42, start: 7, end: 27, trim: true },
      name: "Key",
      purpose: "Authenticate project services",
      operation_id: "op-1",
    });
    view.destroy();
  });

  it("keeps the revision the sheet opened against when the buffer moves underneath", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    let revision = 42;
    const t = {
      ...target(view),
      sync: vi.fn(async () => ({ revision, text: view.state.doc.toString() })),
    };
    const selection = anchorSecretSelection(t, 7, 27);
    await selection.preview(spies.client, true);

    // Another writer lands a line above the value while the sheet is open.
    view.dispatch({ changes: { from: 0, insert: "# generated\n" } });
    revision = 43;
    await selection.mark(spies.client, markArgs);

    // The mark names the revision the offsets were taken from, so the host,
    // which no longer holds it, refuses instead of protecting shifted bytes.
    expect(t.sync).toHaveBeenCalledTimes(1);
    expect(spies.markEditorSecret.mock.calls[0]![2]).toMatchObject({
      range: { revision: 42, start: 7, end: 27 },
    });
    view.destroy();
  });

  it("reports a revision the host no longer holds as a stale selection", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    spies.markEditorSecret.mockRejectedValueOnce(
      new LycaonApiError("revision moved", 409, "editor_revision_conflict"),
    );
    const selection = anchorSecretSelection(target(view), 7, 27);

    await expect(selection.mark(spies.client, markArgs)).rejects.toThrow(
      SECRET_SPAN_COPY.staleDocument,
    );
    view.destroy();
  });

  it("refuses to mark when the publish never reached the host, then anchors once it does", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    const sync = vi.fn(async () => null as { revision: number; text: string } | null);
    const t = { ...target(view), sync };
    const selection = anchorSecretSelection(t, 7, 27);

    await expect(selection.mark(spies.client, markArgs)).rejects.toThrow(
      /have not reached the host/,
    );
    expect(spies.markEditorSecret).not.toHaveBeenCalled();

    sync.mockResolvedValueOnce({ revision: 44, text: view.state.doc.toString() });
    await selection.mark(spies.client, markArgs);
    expect(spies.markEditorSecret.mock.calls[0]![2]).toMatchObject({
      range: { revision: 44, start: 7, end: 27 },
    });
    view.destroy();
  });

  it("refuses when the host ended up holding different bytes", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    // The host's copy is one keystroke behind what the user selected.
    const t = {
      ...target(view),
      sync: vi.fn(async () => ({ revision: 41, text: 'key = "AKIAQYJK5TXV4NZR7SG"' })),
    };
    const selection = anchorSecretSelection(t, 7, 27);

    await expect(selection.mark(spies.client, markArgs)).rejects.toThrow(
      /have not reached the host/,
    );
    await expect(selection.preview(spies.client, true)).rejects.toThrow(
      /have not reached the host/,
    );
    expect(spies.markEditorSecret).not.toHaveBeenCalled();
    expect(spies.previewEditorSecretMark).not.toHaveBeenCalled();
    view.destroy();
  });

  it("counts offsets over the published text, not a newer buffer", async () => {
    // The published text carries an emoji the offsets have to walk past.
    const published = 'a = "🔑"; k = "AKIAQYJK5TXV4NZR7SGB"';
    const view = viewWith(published);
    const spies = clientSpies();
    const t = { ...target(view), sync: vi.fn(async () => ({ revision: 7, text: published })) };
    const from = published.indexOf("AKIA");

    await anchorSecretSelection(t, from, from + 20).mark(spies.client, markArgs);

    expect(spies.markEditorSecret.mock.calls[0]![2]).toMatchObject({
      range: { revision: 7, start: from - 1, end: from + 19, trim: true },
    });
    view.destroy();
  });

  it("never sends the secret value to the host", async () => {
    const view = viewWith('key = "AKIAQYJK5TXV4NZR7SGB"');
    const spies = clientSpies();
    const selection = anchorSecretSelection(target(view), 7, 27);

    await selection.preview(spies.client, true);
    await selection.mark(spies.client, markArgs);

    for (const call of [
      spies.previewEditorSecretMark.mock.calls[0],
      spies.markEditorSecret.mock.calls[0],
    ]) {
      expect(JSON.stringify(call)).not.toContain("AKIAQYJK5TXV4NZR7SGB");
    }
    view.destroy();
  });
});
