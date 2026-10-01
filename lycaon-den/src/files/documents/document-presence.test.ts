// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import * as Y from "yjs";
import { createSignal } from "solid-js";
import type { ItemWindowView } from "../../platform/windows/item-windows.ts";
import { EditorState, EditorSelection } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import type { UpdateEditorDocumentPresenceRequest } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { DocumentFixture } from "../../test/document-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { DocumentReplica } from "./document-replica.ts";
import { encodeUpdate } from "./document-outbox.ts";
import { getOverviewWindowSelections } from "../../components/source/annotations/overview-window-selections.ts";
import { documentPresence } from "./document-presence.ts";

vi.mock("../../platform/connection/client-identity.ts", () => ({ clientIdentity: () => "local" }));
const native = vi.hoisted(() => ({ views: (): readonly ItemWindowView[] => [] }));
vi.mock("../../platform/windows/item-windows.ts", () => ({ itemWindowViews: () => native.views() }));
const cleanup: (() => void | Promise<void>)[] = [];
afterEach(async () => { for (const stop of cleanup.splice(0).reverse()) await stop(); native.views = () => []; });

async function fixture() {
  const host = new DocumentFixture("first line\nsecond line\nthird line");
  const publish = vi.fn(async (_project: string, _document: string, _presence: UpdateEditorDocumentPresenceRequest): Promise<void> => undefined);
  const leave = vi.fn(async () => undefined);
  const client = stubClient({ syncEditorDocument: host.sync, publishEditorDocumentPresence: publish, leaveEditorDocument: leave });
  const outbox = memoryDocumentOutbox();
  const replica = new DocumentReplica(host.snapshot(), () => client, () => undefined, outbox);
  await replica.initialize();
  cleanup.push(() => replica.close());
  const parent = document.createElement("div"); document.body.append(parent);
  const view = new EditorView({ parent, state: EditorState.create({ doc: replica.text.toString(), extensions: [EditorState.allowMultipleSelections.of(true), documentPresence(replica)] }) });
  let closed = false;
  const destroy = () => { if (!closed) { closed = true; view.destroy(); parent.remove(); } };
  cleanup.push(destroy);
  const position = (index: number) => encodeUpdate(Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(replica.text, index)));
  const peer = (client_id: string, window_number: number, ranges: [number, number][]) => ({ client_id, person_id: "person", window_number,
    main: ranges.length - 1, ranges: ranges.map(([anchor, head]) => ({ anchor: position(anchor), head: position(head) })) });
  return { replica, view, host, publish, peer, destroy, leave, outbox };
}

describe("window cursor presence", () => {
  it("renders every peer range without changing local selection and removes departed peers", async () => {
    const { replica, view, host, peer } = await fixture();
    const right = peer("right", 3, [[23, 28]]);
    const participants = [peer("left", 2, [[0, 5], [11, 17]]), right];
    view.dispatch({ selection: { anchor: 8 } });
    replica.receive({ ...host.snapshot(), participants });
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(3));
    expect(view.dom.querySelector(".cm-content")?.textContent).toBe("first linesecond linethird line");
    expect(view.state.selection.main.head).toBe(8);
    replica.receive({ ...host.snapshot(), participants: [right] });
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(1));
    expect(view.dom.querySelector(".cm-document-caret")?.getAttribute("aria-label")).toBe("Browser window 3");
  });

  it("removes closed native windows immediately and ignores their delayed presence", async () => {
    const [views, setViews] = createSignal<ItemWindowView[]>([2, 7, 9].map(number => ({
      label: `file:${number}`, title: "shared.txt", viewNumber: number, kind: "file", projectId: "project",
    })));
    native.views = views;
    const { replica, view, host, peer } = await fixture();
    const participants = [peer("window:file:2", 22, [[0, 5]]), peer("window:file:7", 27, [[11, 17]]),
      peer("window:file:9", 29, [[23, 28]])];
    const stale = { ...host.snapshot(), participants };
    replica.receive(stale);
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(3));
    setViews(views().filter(window => window.viewNumber === 9));
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(1));
    replica.receive({ ...host.snapshot(), participants: [] });
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(0));
    replica.receive(stale);
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(1));
    expect(view.dom.querySelector(".cm-document-caret")?.getAttribute("aria-label")).toBe("Window 9");
    expect(getOverviewWindowSelections(view.state).map(selection => selection.clientId)).toEqual(["window:file:9"]);
    expect(view.dom.textContent).not.toContain("Connecting window");
    setViews([]);
    await vi.waitFor(() => expect(view.dom.querySelectorAll(".cm-document-caret")).toHaveLength(0));
    expect(getOverviewWindowSelections(view.state)).toEqual([]);
  });

  it("publishes multiple selections and clears its visible cursor on detach", async () => {
    const { replica, destroy, publish } = await fixture();
    replica.publishPresence(EditorSelection.create([EditorSelection.range(0, 5), EditorSelection.cursor(11)], 1));
    await vi.waitFor(() => expect(publish).toHaveBeenCalled());
    expect(publish.mock.calls[publish.mock.calls.length - 1]).toBeDefined();
    const last = publish.mock.calls[publish.mock.calls.length - 1];
    expect(last?.[2].ranges).toHaveLength(2);
    expect(last?.[2].main).toBe(1);
    destroy();
    await vi.waitFor(() => {
      const call = publish.mock.calls[publish.mock.calls.length - 1];
      expect(call?.[2].ranges).toEqual([]);
    });
  });

  it("ignores unresolved positions until their document content arrives", async () => {
    const { replica, view, host } = await fixture();
    replica.receive({ ...host.snapshot(), participants: [{ client_id: "peer", person_id: "person", window_number: 8, main: 0, ranges: [{ anchor: "invalid", head: "invalid" }] }] });
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(view.dom.querySelector(".cm-document-caret")).toBeNull();
    expect(view.state.doc.toString()).toBe(host.text.toString());
  });

  it("coalesces movement behind an in-flight update so clearing cannot be overtaken", async () => {
    const { replica, publish } = await fixture();
    let release: () => void = () => undefined;
    publish.mockImplementationOnce(() => new Promise<void>(resolve => { release = resolve; }));
    replica.publishPresence(EditorSelection.single(0, 5));
    await vi.waitFor(() => expect(publish).toHaveBeenCalledTimes(1));
    replica.publishPresence(EditorSelection.single(11, 17));
    replica.clearPresence();
    expect(publish).toHaveBeenCalledTimes(1);
    release();
    await vi.waitFor(() => expect(publish).toHaveBeenCalledTimes(2));
    expect(publish.mock.calls[1]?.[2].ranges).toEqual([]);
  });

  it("rejoins an expired participant and immediately republishes its selection", async () => {
    const { replica, publish } = await fixture();
    const synchronize = vi.spyOn(replica, "synchronize");
    publish.mockRejectedValueOnce(new LycaonApiError("Expired participant", 409, "editor_replica_identity"));
    replica.publishPresence(EditorSelection.single(11, 17));
    await vi.waitFor(() => expect(publish).toHaveBeenCalledTimes(2));
    expect(synchronize).toHaveBeenCalledOnce();
    expect(publish.mock.calls[1]?.[2].ranges).toHaveLength(1);
  });

  it("does not reschedule presence while close waits for durable preservation", async () => {
    const { replica, destroy, outbox, publish, leave } = await fixture();
    let release = () => {};
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const commit = outbox.commit.bind(outbox);
    vi.spyOn(outbox, "commit").mockImplementationOnce(async (...args) => { await barrier; await commit(...args); });
    replica.replaceLocal("preserved on close");
    const suspended = replica.suspend(() => true);
    const closing = replica.close();
    const disposed = vi.spyOn(replica.doc, "destroy");
    destroy();
    try {
      // Closing awaits storage; detaching must still yield to the next task.
      await new Promise(resolve => setTimeout(resolve, 0));
      expect(publish).not.toHaveBeenCalled();
      expect(leave).not.toHaveBeenCalled();
    } finally {
      release();
      await closing;
      expect(await suspended).toBe(true);
    }
    expect(disposed).toHaveBeenCalledOnce();
    await vi.waitFor(() => expect(leave).toHaveBeenCalledOnce());
  });

  it("waits for a pending rejoin before departing the closed editor instance", async () => {
    const { replica, publish, leave } = await fixture();
    let release: () => void = () => undefined;
    const synchronize = vi.spyOn(replica, "synchronize").mockImplementationOnce(() => new Promise<void>(resolve => { release = resolve; }));
    publish.mockRejectedValueOnce(new LycaonApiError("Expired participant", 409, "editor_replica_identity"));
    replica.publishPresence(EditorSelection.single(0, 5));
    await vi.waitFor(() => expect(synchronize).toHaveBeenCalledOnce());
    const closing = replica.close();
    expect(leave).not.toHaveBeenCalled();
    release();
    await closing;
    await vi.waitFor(() => expect(leave).toHaveBeenCalledWith(replica.accepted.project_id, replica.accepted.id,
      { client_id: replica.clientId, incarnation: replica.incarnation }));
    expect(publish).toHaveBeenCalledTimes(1);
  });
});
