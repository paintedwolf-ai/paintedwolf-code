import { loadedBuffer, PROJECT, ROOTS, resetProjectFilesViewTest } from "./project-files-view-test-harness.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { getIndentUnit } from "@codemirror/language";
import { EditorView } from "@codemirror/view";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, expect, it, vi } from "vitest";
import { saveEditorLineLayout } from "../../settings/editor/editor-prefs.ts";
import { createAppStore } from "../../store/app-state.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";

vi.mock("../../utils/clipboard.ts", async original => ({
  ...await original<typeof import("../../utils/clipboard.ts")>(),
  writeClipboardText: vi.fn(async () => {}),
}));

beforeEach(() => {
  resetProjectFilesViewTest();
  vi.clearAllMocks();
});

async function open(path: string, content: string) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  const client = getLycaonClient()!;
  const source = await client.getProjectSource(PROJECT, path, { rootId: "r1" });
  vi.spyOn(client, "getProjectSource").mockResolvedValue({ ...source, content, size_bytes: content.length });
  loadedBuffer({ path, content, sha256: source.sha256 });
  render(() => <ProjectFilesView projectId={PROJECT} appStore={appStore} roots={ROOTS} client={client} />);
  const host = await screen.findByTestId("files-editor-host");
  await waitFor(() => expect(host.querySelector(".cm-content")).toBeTruthy());
  return EditorView.findFromDOM(host)!;
}

it("copies the whole Pretty document from the toolbar and the captured selection from the editor menu", async () => {
  const view = await open("sample.json", "[1,2]");
  await saveEditorLineLayout("pretty");
  const indent = " ".repeat(getIndentUnit(view.state));
  const whole = `[\n${indent}1,\n${indent}2\n]`;
  view.dispatch({ selection: { anchor: 1, head: 4 } });
  fireEvent.contextMenu(screen.getByTestId("files-editor-copy"));
  fireEvent.click(await screen.findByTestId("files-editor-copy-as-shown"));
  expect(writeClipboardText).toHaveBeenLastCalledWith(whole);
  fireEvent.contextMenu(view.contentDOM);
  const selected = await screen.findByTestId("files-ctx-editor-copy-as-shown");
  view.dispatch({ selection: { anchor: 0 } });
  fireEvent.click(selected);
  expect(writeClipboardText).toHaveBeenLastCalledWith(`1,\n${indent}2`);
  fireEvent.contextMenu(view.contentDOM);
  fireEvent.click(await screen.findByTestId("files-ctx-editor-copy-as-shown"));
  expect(writeClipboardText).toHaveBeenLastCalledWith(whole);
});

it.each([
  ["plain.txt", "ordinary prose", "pretty"],
  ["sample.json", "[1,2]", "off"],
] as const)("omits Pretty copy on both menus for %s in %s with layout %s", async (path, text, layout) => {
  const view = await open(path, text);
  await saveEditorLineLayout(layout);
  fireEvent.contextMenu(screen.getByTestId("files-editor-copy"));
  const contents = await screen.findByTestId("files-editor-copy-contents");
  expect(screen.queryByTestId("files-editor-copy-as-shown")).toBeNull();
  fireEvent.click(contents);
  fireEvent.contextMenu(view.contentDOM);
  expect(await screen.findByTestId("text-edit-copy")).toBeTruthy();
  expect(screen.queryByTestId("files-ctx-editor-copy-as-shown")).toBeNull();
});
