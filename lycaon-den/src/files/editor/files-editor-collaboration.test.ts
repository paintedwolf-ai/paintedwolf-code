// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { EditorSelection, EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentReplica } from "../documents/document-replica.ts";
import { filesCollaborationExtension, rebindFilesCollaboration } from "./files-editor-collaboration.ts";

describe("editor collaboration attachment", () => {
  it.each(["", "prefix\n"])("preserves live selections when late attachment adds %j", async (prefix) => {
    const host = new DocumentFixture(prefix + "ab\ncd");
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, memoryDocumentOutbox());
    await replica.initialize();
    const view = new EditorView({ state: EditorState.create({ doc: "ab\ncd",
      extensions: [EditorState.allowMultipleSelections.of(true), filesCollaborationExtension(undefined)] }) });
    try {
      view.dispatch({ selection: EditorSelection.create([EditorSelection.cursor(1), EditorSelection.range(4, 3)], 1) });
      rebindFilesCollaboration(view, replica);
      expect(view.state.doc.toString()).toBe(prefix + "ab\ncd");
      expect(view.state.selection.toJSON()).toEqual(EditorSelection.create([
        EditorSelection.cursor(prefix.length + 1), EditorSelection.range(prefix.length + 4, prefix.length + 3),
      ], 1).toJSON());
      expect(replica.history.state.selection.eq(view.state.selection)).toBe(true);
    } finally { view.destroy(); await replica.close(); }
  });

  it("upgrades the painted view, recovers edits, and detaches a retired replica", async () => {
    const host = new DocumentFixture("recovered text");
    const client = stubClient({ syncEditorDocument: host.sync, submitEditorDocumentUpdate: host.submit });
    const replica = new DocumentReplica(host.snapshot(), () => client, () => {}, memoryDocumentOutbox());
    await replica.initialize();
    const parent = document.createElement("div");
    document.body.append(parent);
    const view = new EditorView({ parent, state: EditorState.create({ doc: "source text", extensions: filesCollaborationExtension(undefined) }) });
    const content = view.contentDOM;
    try {
      rebindFilesCollaboration(view, replica);
      expect(view.contentDOM).toBe(content);
      expect(view.state.doc.toString()).toBe("recovered text");
      view.dispatch({ changes: { from: view.state.doc.length, insert: " edited" }, userEvent: "input.type" });
      await replica.flush();
      expect(host.text.toString()).toBe("recovered text edited");
      rebindFilesCollaboration(view, undefined);
      await replica.close();
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: "replacement" } });
      expect(view.state.doc.toString()).toBe("replacement");
      expect(view.contentDOM).toBe(content);
    } finally {
      view.destroy();
      parent.remove();
      await replica.close();
    }
  });
});
