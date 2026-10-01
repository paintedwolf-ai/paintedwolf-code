import * as Y from "yjs";
import type { EditorDocument, EditorReplicaFrame, SubmitEditorDocumentUpdateRequest, SyncEditorDocumentRequest } from "../api/types.ts";
import { decodeUpdate, encodeUpdate } from "../files/documents/document-outbox.ts";

export class DocumentFixture {
  doc = new Y.Doc({ gc: false });
  get text(): Y.Text { return this.doc.getText("text"); }
  readonly wire: EditorDocument;
  private nextReplica = 10;
  private readonly incarnations = new Map<string, number>();
  private readonly receipts = new Map<string, number>();

  constructor(content = "base", fields: Partial<EditorDocument> = {}) {
    this.doc.clientID = 1;
    this.text.insert(0, content);
    this.wire = {
      id: "document-1", project_id: "project-1", workspace_id: "workspace-1", file_id: "file-1", root_id: "root-1", path: "a.txt",
      base_content: content, base_sha256: "base-sha", size_bytes: content.length,
      encoding: "utf-8", eol: "lf", base_eol: "lf", mixed_eol: false, base_mixed_eol: false,
      revision: 1, dirty: false, diverged: false, absent: false, secret_screen_status: "pending",
      epoch: 1, state_vector: "", crdt_update: "", published_revision: 1, participants: [], ...fields,
    };
  }

  /** The wire snapshot with the text its state carries, as a replica holds it. */
  accepted(vector?: string): EditorDocument {
    const snapshot = this.snapshot(vector);
    const draft = this.text.toString();
    return { ...snapshot, base_content: snapshot.base_content ?? draft };
  }

  snapshot(vector?: string): EditorDocument {
    return { ...this.wire, state_vector: encodeUpdate(Y.encodeStateVector(this.doc)),
      crdt_update: encodeUpdate(Y.encodeStateAsUpdate(this.doc, vector ? decodeUpdate(vector) : undefined)),
      participants: [...this.wire.participants] };
  }

  frame(vector?: string, baseSHA256?: string): EditorReplicaFrame {
    const frame: Partial<EditorDocument> = this.snapshot(vector);
    if (baseSHA256 === frame.base_sha256) delete frame.base_content;
    else frame.base_content = this.wire.base_content ?? this.text.toString();
    return frame as EditorReplicaFrame;
  }

  sync = async (_project: string, _id: string, request: SyncEditorDocumentRequest): Promise<EditorReplicaFrame> => {
    let replica = this.incarnations.get(request.incarnation);
    if (!replica) { replica = ++this.nextReplica; this.incarnations.set(request.incarnation, replica); }
    return { ...this.frame(request.epoch === this.wire.epoch ? request.state_vector : undefined, request.base_sha256), replica_id: replica };
  };

  submit = async (_project: string, _id: string, request: SubmitEditorDocumentUpdateRequest): Promise<EditorReplicaFrame> => {
    let revision = this.receipts.get(request.operation_id);
    if (!revision) {
      Y.applyUpdate(this.doc, decodeUpdate(request.update), "remote");
      revision = ++this.wire.revision;
      this.wire.dirty = this.text.toString() !== this.wire.base_content;
      this.receipts.set(request.operation_id, revision);
    }
    return { ...this.frame(request.state_vector, request.base_sha256), accepted_revision: revision, accepted_operation_id: request.operation_id };
  };

  import(content: string): EditorDocument {
    this.doc.destroy();
    this.doc = new Y.Doc({ gc: false });
    this.doc.clientID = 1;
    this.text.insert(0, content);
    this.wire.epoch++;
    this.wire.revision++;
    this.wire.base_content = content;
    this.wire.dirty = false;
    return this.snapshot();
  }

  replace(content: string): EditorDocument {
    this.doc.transact(() => { this.text.delete(0, this.text.length); this.text.insert(0, content); });
    this.wire.revision++;
    return this.snapshot();
  }
}
