import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { TranscriptDiffGroup } from "./TranscriptDiffGroup.tsx";
import { foldFileEdits } from "../../chat/file-edit/file-edit-fold.ts";
import type { FileEditStep } from "../../chat/file-edit/file-edit-fold.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { resetFindRevealHostsForTests } from "../../find/find-reveal.ts";
import { resetFindControllerForTests } from "../../find/find-controller.ts";
import { createTranscriptDisclosureStore, TranscriptDisclosureProvider } from "../../chat/transcript/presentation/disclosure-state.tsx";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";

const client = stubFilesClient();
vi.mock("../../platform/connection/app-connection.ts", async original => ({
  ...await original<typeof import("../../platform/connection/app-connection.ts")>(), getLycaonClient: () => client,
}));

afterEach(() => {
  clearTranscriptEntryMemory();
  resetFindControllerForTests();
  resetFindRevealHostsForTests();
  document.body.replaceChildren();
});

function step(
  key: string,
  path: string,
  before: string | null,
  after: string,
): FileEditStep {
  return { key, snapshot: fileEditPreviewFixture({ path, before, after }), tool: "edit" };
}

const RUN = [
  step("t1", "src/pipeline.py", "one\n", "two\n"),
  step("t2", "src/cli.py", "a\n", "b\nc\n"),
  step("t3", "src/pipeline.py", "two\n", "three\nfour\n"),
  step("t4", "tests/test_cli.py", null, "x\ny\nz\n"),
];

function renderGroup() {
  const folds = foldFileEdits(RUN);
  return render(() => (
    <TranscriptDiffGroup
      folds={folds}
      layout="chat"
      sessionId="s1"
      entryKey="group-1"
      projectId="project"
    />
  ));
}

describe("TranscriptDiffGroup", () => {
  it.each([1, 3])("retains each child choice when a %s-file group remounts", (count) => {
    const store = createTranscriptDisclosureStore();
    const folds = foldFileEdits(RUN).slice(0, count);
    const mount = () => render(() => <TranscriptDisclosureProvider value={store}>
      <TranscriptDiffGroup folds={folds} layout="chat" entryKey="held-group" />
    </TranscriptDisclosureProvider>);
    let view = mount();
    screen.getByTestId("diff-group").querySelector<HTMLElement>(":scope > summary")!.click();
    const chosen = folds.at(-1)!;
    if (count > 1) screen.getByLabelText(`Show diff for ${chosen.path}`).click();
    expect(store.isOpen(transcriptDisclosureKey.diffFile(chosen.key))).toBe(true);
    view.unmount();
    view = mount();
    expect((screen.getByTestId("diff-group") as HTMLDetailsElement).open).toBe(true);
    expect(screen.getByLabelText(`Collapse diff for ${chosen.path}`)).toBeTruthy();
    screen.getByLabelText(`Collapse diff for ${chosen.path}`).click();
    view.unmount();
    mount();
    expect(screen.getByLabelText(`Show diff for ${chosen.path}`)).toBeTruthy();
    expect(store.isOpen(transcriptDisclosureKey.diffFile(chosen.key))).toBe(false);
  });

  it("leaves the tool row that wrote a file to its own state", () => {
    const store = createTranscriptDisclosureStore();
    const folds = foldFileEdits(RUN);
    render(() => <TranscriptDisclosureProvider value={store}>
      <TranscriptDiffGroup folds={folds} layout="chat" entryKey="shared-group" />
    </TranscriptDisclosureProvider>);
    screen.getByTestId("diff-group").querySelector<HTMLElement>(":scope > summary")!.click();
    // A fold is keyed by the first write to its file, which is also that write's tool row.
    const writer = transcriptDisclosureKey.tool(folds[0]!.key);
    store.setUserOpen(writer, true);
    expect(store.isOpen(writer)).toBe(true);
    expect(store.isOpen(transcriptDisclosureKey.diffFile(folds[0]!.key))).toBe(false);
  });

  it("opens the sole diff when its group is opened", () => {
    render(() => (
      <TranscriptDiffGroup
        folds={foldFileEdits([RUN[0]!])}
        layout="chat"
        sessionId="s1"
        entryKey="single-diff"
      />
    ));

    const group = screen.getByTestId("diff-group") as HTMLDetailsElement;
    expect(group.open).toBe(false);
    expect(group.getAttribute("data-files")).toBe("1");
    expect(group.querySelector("summary")?.getAttribute("aria-label")).toBe(
      "1 file changed, 1 added, 1 removed, 1 write",
    );
    expect(screen.getAllByTestId("file-edit-diff")).toHaveLength(1);

    group.querySelector<HTMLElement>(":scope > summary")!.click();
    expect(group.open).toBe(true);
    expect(screen.getByTestId("file-edit-diff").getAttribute("data-expanded")).toBe(
      "true",
    );
  });

  it("summarizes net changes across the group", async () => {
    renderGroup();
    const group = screen.getByTestId("diff-group");
    expect(group.getAttribute("data-files")).toBe("3");
    expect(screen.getByText("3 files changed")).toBeTruthy();
    // pipeline +2 −1, cli +2 −1, new test file +3 −0.
    await waitFor(() => expect(screen.getByText("+7")).toBeTruthy());
    expect(screen.getByText("−2")).toBeTruthy();
    expect(screen.getByText("4 writes")).toBeTruthy();
  });

  it("loads net totals while collapsed", async () => {
    renderGroup();
    const group = screen.getByTestId("diff-group") as HTMLDetailsElement;
    expect(group.open).toBe(false);
    await waitFor(() => expect(group.querySelector("summary")?.getAttribute("aria-label")).toBe(
      "3 files changed, 7 added, 2 removed, 4 writes",
    ));
  });

  it("leaves rows collapsed when a group has multiple diffs", () => {
    renderGroup();
    const group = screen.getByTestId("diff-group") as HTMLDetailsElement;

    group.querySelector<HTMLElement>(":scope > summary")!.click();
    expect(group.open).toBe(true);
    expect(
      screen
        .getAllByTestId("file-edit-diff")
        .map((row) => row.getAttribute("data-expanded")),
    ).toEqual(["false", "false", "false"]);
  });

  it("lists files in first-touch order, one row each", () => {
    renderGroup();
    const paths = screen
      .getAllByTestId("file-edit-diff")
      .map((row) => row.getAttribute("data-path"));
    expect(paths).toEqual(["src/pipeline.py", "src/cli.py", "tests/test_cli.py"]);
  });

  it("gives every row the manifest chrome, closed and unopened", () => {
    renderGroup();
    for (const row of screen.getAllByTestId("file-edit-diff")) {
      expect(row.getAttribute("data-chrome")).toBe("row");
      expect(row.getAttribute("data-expanded")).toBe("false");
      expect(row.querySelector(".den-row-mark")).toBeTruthy();
    }
  });

  it("keeps every disclosure on the right — group caret and row marks alike", () => {
    renderGroup();
    const summary = screen
      .getByTestId("diff-group")
      .querySelector(".den-diff-group-summary")!;
    expect(summary.lastElementChild?.className).toContain(
      "den-tool-chicklet-caret",
    );
    for (const row of screen.getAllByTestId("file-edit-diff")) {
      const header = row.querySelector(".den-file-edit-diff-header")!;
      expect(header.lastElementChild?.querySelector(".den-row-mark")).toBeTruthy();
    }
  });

  it("opens one diff at a time", () => {
    renderGroup();
    const rows = () => screen.getAllByTestId("file-edit-diff");
    const openState = () =>
      rows().map((row) => row.getAttribute("data-expanded"));

    screen.getByLabelText("Show diff for src/pipeline.py").click();
    expect(openState()).toEqual(["true", "false", "false"]);

    screen.getByLabelText("Show diff for tests/test_cli.py").click();
    expect(openState()).toEqual(["false", "false", "true"]);

    screen.getByLabelText("Collapse diff for tests/test_cli.py").click();
    expect(openState()).toEqual(["false", "false", "false"]);
  });

  it("keeps every row's own way into Files", () => {
    const folds = foldFileEdits(RUN);
    render(() => (
      <TranscriptDiffGroup
        folds={folds}
        layout="chat"
        sessionId="s1"
        projectId="p1"
        entryKey="group-2"
      />
    ));
    const links = screen
      .getAllByTestId("source-path-link")
      .map((el) => el.getAttribute("data-den-source-path"));
    expect(links).toEqual(["src/pipeline.py", "src/cli.py", "tests/test_cli.py"]);
  });

  it("does not toggle a diff when the path is clicked on its way to Files", () => {
    const folds = foldFileEdits(RUN);
    render(() => (
      <TranscriptDiffGroup
        folds={folds}
        layout="chat"
        sessionId="s1"
        projectId="p1"
        entryKey="group-3"
      />
    ));
    screen.getAllByTestId("source-path-link")[0]!.click();
    expect(
      screen
        .getAllByTestId("file-edit-diff")
        .map((row) => row.getAttribute("data-expanded")),
    ).toEqual(["false", "false", "false"]);
  });

  it("toggles from anywhere else on the row", () => {
    renderGroup();
    const row = screen.getAllByTestId("file-edit-diff")[1]!;
    row.querySelector<HTMLElement>(".den-file-edit-diff-header")!.click();
    expect(row.getAttribute("data-expanded")).toBe("true");
  });

  it("re-opens the sole diff when its group is collapsed and re-opened", () => {
    render(() => (
      <TranscriptDiffGroup
        folds={foldFileEdits([RUN[0]!])}
        layout="chat"
        sessionId="s1"
        entryKey="single-diff-reopen"
      />
    ));

    const group = screen.getByTestId("diff-group") as HTMLDetailsElement;
    const summary = group.querySelector<HTMLElement>(":scope > summary")!;

    summary.click();
    expect(group.open).toBe(true);
    expect(screen.getByTestId("file-edit-diff").getAttribute("data-expanded")).toBe("true");

    summary.click();
    expect(group.open).toBe(false);

    summary.click();
    expect(group.open).toBe(true);
    expect(screen.getByTestId("file-edit-diff").getAttribute("data-expanded")).toBe("true");
  });
});
