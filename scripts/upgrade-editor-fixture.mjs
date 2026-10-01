// Exercise the shipped Yjs and CodeMirror codecs in retained upgrade fixtures.
import fs from "node:fs/promises";
import path from "node:path";
import { createHash, randomUUID } from "node:crypto";
import { createRequire } from "node:module";
const require = createRequire(new URL("../lycaon-den/package.json", import.meta.url));
const Y = require("yjs");
const { EditorState, Transaction } = require("@codemirror/state");
const { history, historyField, isolateHistory, undo, redo } = require("@codemirror/commands");
const layout = JSON.parse(await fs.readFile(new URL("../lycaon/internal/editoroutbox/format.json", import.meta.url), "utf8"));
const digest = (value) => createHash("sha256").update(value).digest("hex");
/** The accepted text a wire document carries inside its CRDT state. */
function textOf(document) {
  const state = new Y.Doc();
  Y.applyUpdate(state, decode(document.crdt_update));
  const text = state.getText("text").toString();
  state.destroy();
  return text;
}
/** Records the published header names inside the record log. */
async function preservedRecords(log, evidence) {
  const header = JSON.parse(await fs.readFile(path.join(configDir, path.dirname(evidence.outbox_path), layout.header), "utf8"));
  return header.frames.map((frame) => JSON.parse(log.subarray(frame.offset, frame.offset + frame.length)));
}
const encode = (value) => Buffer.from(value).toString("base64");
const decode = (value) => new Uint8Array(Buffer.from(value, "base64"));
const [mode, base, configDir, manifestPath, projectRoot] = process.argv.slice(2);
const manifest = JSON.parse(await fs.readFile(manifestPath, "utf8"));
const token = (await fs.readFile(path.join(configDir, "api.token"), "utf8")).trim();
const projectPath = `/v1/projects/${manifest.project_id}`;
async function request(suffix, body) {
  const response = await fetch(base + projectPath + suffix, { method: body === undefined ? "GET" : "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  if (!response.ok) throw new Error(`Editor fixture ${suffix}: HTTP ${response.status}: ${await response.text()}`);
  return response.json();
}

if (mode === "seed") {
  await fs.writeFile(path.join(projectRoot, "editor-fixture.txt"), "editor base\n");
  const project = await request("");
  const clientId = "window:upgrade-fixture";
  let document = await request("/editor-documents", { path: "editor-fixture.txt", root_id: project.roots[0].id, client_id: clientId });
  const endpoint = `/editor-documents/${document.id}`;
  const doc = new Y.Doc();
  Y.applyUpdate(doc, decode(document.crdt_update));
  const incarnation = randomUUID();
  const joined = await request(endpoint + "/sync", { client_id: clientId, incarnation, epoch: document.epoch,
    state_vector: encode(Y.encodeStateVector(doc)), base_sha256: document.base_sha256 });
  doc.clientID = joined.replica_id;
  Y.applyUpdate(doc, decode(joined.crdt_update));
  let editor = EditorState.create({ doc: doc.getText("text").toString(), extensions: [history()] });
  let sequence = 0;
  function edit(text) {
    const vector = Y.encodeStateVector(doc);
    const start = doc.getText("text").length;
    doc.getText("text").insert(start, text);
    editor = editor.update({ changes: { from: start, insert: text }, annotations: [isolateHistory.of("full"), Transaction.userEvent.of("input.type")] }).state;
    return { kind: "update", documentId: document.id, projectId: manifest.project_id, rootId: document.root_id, fileId: document.file_id, path: document.path, clientId, replicaId: doc.clientID,
      epoch: document.epoch, operationId: randomUUID(), update: encode(Y.encodeStateAsUpdate(doc, vector)), acknowledged: false,
      sequence: ++sequence, sessionId: manifest.session_id };
  }
  async function accept(record) {
    const frame = await request(endpoint + "/updates", { client_id: clientId, replica_id: record.replicaId, epoch: record.epoch,
      operation_id: record.operationId, update: record.update, session_id: record.sessionId });
    Y.applyUpdate(doc, decode(frame.crdt_update));
    document = { ...document, ...frame, draft: doc.getText("text").toString() };
  }
  await accept(edit("saved window edit\n"));
  const saveOperation = randomUUID();
  const pinned = await request(endpoint + "/snapshots", { client_id: clientId, operation_id: saveOperation });
  document = await request(endpoint + "/save", { client_id: clientId, expected_revision: pinned.revision, operation_id: saveOperation, session_id: manifest.session_id });
  const savedText = document.base_content ?? textOf(document);
  const accepted = edit("unsaved host edit\n");
  await accept(accepted);
  const pendingSave = randomUUID();
  const reserved = await request(endpoint + "/snapshots", { client_id: clientId, operation_id: pendingSave });
  const acceptedText = doc.getText("text").toString();
  const { draft: _draft, base_content: _base, command_history: _command, ...confirmed } = document;
  confirmed.crdt_update = encode(Y.encodeStateAsUpdate(doc));
  confirmed.state_vector = "";
  confirmed.participants = [];
  const pending = edit("offline window edit\n");
  const { doc: _doc, ...savedHistory } = editor.toJSON({ history: historyField });
  const checkpoint = { kind: "checkpoint", documentId: document.id, projectId: manifest.project_id, rootId: document.root_id,
    fileId: document.file_id, path: document.path, clientId, epoch: document.epoch, state: encode(Y.encodeStateAsUpdate(doc)),
    replicaId: doc.clientID, incarnation, confirmed, history: savedHistory, synchronized: false, pendingOperations: [pending.operationId] };
  const command = { kind: "command", action: "save", documentId: document.id, projectId: manifest.project_id, rootId: document.root_id, fileId: document.file_id, path: document.path, clientId,
    operationId: pendingSave, sessionId: manifest.session_id, createdAt: new Date().toISOString(),
    request: { client_id: clientId, operation_id: pendingSave, expected_revision: reserved.revision, session_id: manifest.session_id } };
  const frames = [];
  const chunks = [];
  let offset = 0;
  for (const record of [checkpoint, pending, command]) {
    const raw = Buffer.from(JSON.stringify(record) + "\n");
    const operation = record.kind === "checkpoint" ? "checkpoint" : record.operationId;
    frames.push({ name: digest(`${record.clientId}\0${record.kind}\0${operation}`), kind: record.kind, offset, length: raw.length, sha256: digest(raw),
      ...(record.kind === "checkpoint" ? { pending: record.pendingOperations, synchronized: record.synchronized } : {}) });
    chunks.push(raw);
    offset += raw.length;
  }
  const raw = Buffer.concat(chunks);
  const log = `${layout.log_prefix}1${layout.log_suffix}`;
  const header = { format: layout.version, address: Object.fromEntries(["documentId", "projectId", "rootId", "fileId", "path"].map(k => [k, checkpoint[k]])),
    updatedAt: Math.floor(Date.now() / 1000), synchronized: false, retainedClients: [clientId], log, logBytes: raw.length, frames };
  const relative = path.join(layout.directory, digest(document.id), log);
  await fs.mkdir(path.dirname(path.join(configDir, relative)), { recursive: true, mode: 0o700 });
  await fs.writeFile(path.join(configDir, relative), raw, { mode: 0o600 });
  await fs.writeFile(path.join(configDir, path.dirname(relative), layout.header), JSON.stringify(header), { mode: 0o600 });
  manifest.editor_history = { document_id: document.id, client_id: clientId, epoch: document.epoch,
    accepted_text: acceptedText, local_text: doc.getText("text").toString(), saved_text: savedText,
    save_operation_id: saveOperation, save_revision: pinned.revision, pending_save_operation_id: pendingSave,
    pending_save_revision: reserved.revision, accepted_operation_id: accepted.operationId, pending_operation_id: pending.operationId,
    outbox_path: relative, outbox_sha256: digest(raw), history_sha256: digest(JSON.stringify(checkpoint.history)) };
  await fs.writeFile(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
  doc.destroy();
} else if (mode === "verify") {
  const evidence = manifest.editor_history;
  if (!evidence) throw new Error("Missing collaborative editor fixture evidence");
  const raw = await fs.readFile(path.join(configDir, evidence.outbox_path));
  if (digest(raw) !== evidence.outbox_sha256) throw new Error("Preserved editor bytes changed");
  const records = await preservedRecords(raw, evidence);
  const checkpoint = records.find(r => r.kind === "checkpoint");
  if (digest(JSON.stringify(checkpoint.history)) !== evidence.history_sha256) throw new Error("Window undo history changed");
  const doc = new Y.Doc();
  Y.applyUpdate(doc, decode(checkpoint.state));
  if (doc.getText("text").toString() !== evidence.local_text) throw new Error("Yjs checkpoint lost local edits");
  let editor = EditorState.fromJSON({ ...checkpoint.history, doc: doc.getText("text").toString() }, { extensions: [history()] }, { history: historyField });
  const target = { get state() { return editor; }, dispatch(transaction) { editor = transaction.state; } };
  if (!undo(target) || editor.doc.toString() !== evidence.accepted_text || !redo(target) || editor.doc.toString() !== evidence.local_text) {
    throw new Error("Retained window undo/redo semantics changed");
  }
  const document = await request(`/editor-documents/${evidence.document_id}/snapshots`, { client_id: evidence.client_id });
  if (textOf(document) !== evidence.accepted_text || document.base_content !== evidence.saved_text || !document.dirty) throw new Error("Accepted editor draft or saved base changed");
  const accepted = new Y.Doc();
  Y.applyUpdate(accepted, decode(document.crdt_update));
  const pending = records.find(r => r.kind === "update");
  Y.applyUpdate(accepted, decode(pending.update));
  if (accepted.getText("text").toString() !== evidence.local_text) throw new Error("Pending editor update no longer joins the host state");
  accepted.destroy(); doc.destroy();
  console.log("retained editor draft, pending update, and window undo/redo verified");
} else if (mode === "replay") {
  const evidence = manifest.editor_history;
  const endpoint = `/editor-documents/${evidence.document_id}`;
  const raw = await fs.readFile(path.join(configDir, evidence.outbox_path));
  const records = await preservedRecords(raw, evidence);
  const pending = records.find(record => record.kind === "update");
  const command = records.find(record => record.kind === "command" && record.action === "save");
  const replayOldSave = () => request(endpoint + "/save", { client_id: evidence.client_id,
    operation_id: evidence.save_operation_id, expected_revision: evidence.save_revision,
    session_id: manifest.session_id });
  await replayOldSave();
  const submit = () => request(endpoint + "/updates", { client_id: pending.clientId,
    replica_id: pending.replicaId, epoch: pending.epoch, operation_id: pending.operationId,
    update: pending.update, session_id: pending.sessionId });
  const first = await submit(), duplicate = await submit();
  if (first.accepted_operation_id !== pending.operationId || duplicate.accepted_revision !== first.accepted_revision) {
    throw new Error("Restored update replay lost its acknowledgement identity");
  }
  const saved = await request(endpoint + "/save", command.request);
  if (textOf(saved) !== evidence.local_text || saved.base_content !== evidence.accepted_text || !saved.dirty) {
    throw new Error("Restored save did not publish its reserved revision independently of later typing");
  }
  await request(endpoint + "/save", command.request);
  await replayOldSave();
  const current = await request(endpoint + "/snapshots", { client_id: evidence.client_id });
  const disk = await fs.readFile(path.join(projectRoot, current.path), "utf8");
  if (textOf(current) !== evidence.local_text || current.base_content !== evidence.accepted_text || disk !== evidence.accepted_text) {
    throw new Error("Delayed save replay republished a different draft");
  }
  console.log("restored pending update, exact save reservation, and duplicate delivery verified");
} else {
  throw new Error("Expected seed, verify, or replay editor fixture mode");
}
