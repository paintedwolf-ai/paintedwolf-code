import { describe, expect, it } from "vitest";
import * as Y from "yjs";
import { documentCore } from "../../test/document-core.ts";
import { decodeUpdate, encodeUpdate } from "./document-outbox.ts";

describe("Yjs and the embedded host core", () => {
  it("converges after concurrent Unicode edits, duplicate delivery, undo, and restart", async () => {
    const host = await documentCore();
    expect(host({ action: "open", handle: 1, client: 1 }).error).toBeUndefined();
    const seed = host({ action: "edit", handle: 1, edits: [{ index: 0, delete: 0, insert: "a🐺z\n" }], checkpoint: true });
    const left = new Y.Doc({ gc: false });
    const right = new Y.Doc({ gc: false });
    Y.applyUpdate(left, decodeUpdate(seed.checkpoint));
    Y.applyUpdate(right, decodeUpdate(seed.checkpoint));
    left.clientID = 2;
    right.clientID = 3;
    const undo = new Y.UndoManager(left.getText("text"));
    left.getText("text").insert(1, "left");
    right.getText("text").insert(3, "right");
    const updates = [right, left].map((doc) => encodeUpdate(Y.encodeStateAsUpdate(doc, decodeUpdate(seed.vector))));
    for (const update of [...updates, ...updates]) expect(host({ action: "apply", handle: 1, update }).error).toBeUndefined();
    const accepted = host({ action: "inspect", handle: 1, checkpoint: true });
    expect(accepted.text).toBe("aleft🐺rightz\n");
    Y.applyUpdate(left, decodeUpdate(accepted.checkpoint), "remote");
    Y.applyUpdate(right, decodeUpdate(accepted.checkpoint));
    undo.undo();
    expect(left.getText("text").toString()).toBe("a🐺rightz\n");
    const undone = host({ action: "apply", handle: 1, client: 2,
      update: encodeUpdate(Y.encodeStateAsUpdate(left, decodeUpdate(accepted.vector))), checkpoint: true });
    expect(undone.error).toBeUndefined();
    expect(undone.text).toBe("a🐺rightz\n");
    const restarted = await documentCore();
    const restored = restarted({ action: "open", handle: 1, client: 1, update: undone.checkpoint });
    expect(restored.text).toBe(undone.text);
    undo.destroy(); left.destroy(); right.destroy();
  });

  it("rejects another replica's inserts and non-text shared roots", async () => {
    const host = await documentCore();
    host({ action: "open", handle: 1, client: 1 });
    const peer = new Y.Doc();
    peer.clientID = 7;
    peer.getText("text").insert(0, "forged");
    expect(host({ action: "apply", handle: 1, client: 8,
      update: encodeUpdate(Y.encodeStateAsUpdate(peer)) }).error).toBe("foreign_replica_update");
    host({ action: "drop", handle: 1 });
    host({ action: "open", handle: 1, client: 1 });
    peer.getMap("metadata").set("authority", true);
    expect(host({ action: "apply", handle: 1, client: 7,
      update: encodeUpdate(Y.encodeStateAsUpdate(peer)) }).error).toBe("invalid_schema");
    peer.destroy();
  });
  it("cannot alias a wide replica identity into an assigned 32-bit identity", async () => {
    const host = await documentCore();
    host({ action: "open", handle: 1, client: 1 });
    const peer = new Y.Doc();
    peer.clientID = 2 ** 32 + 7;
    peer.getText("text").insert(0, "forged");
    expect(host({ action: "apply", handle: 1, client: 7,
      update: encodeUpdate(Y.encodeStateAsUpdate(peer)) }).error).toBe("foreign_replica_update");
    peer.destroy();
  });

});
