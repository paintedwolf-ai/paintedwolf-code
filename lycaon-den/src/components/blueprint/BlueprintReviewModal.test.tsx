import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { AppStore } from "../../store/app-state-model.ts";
import { overlayRel } from "../../platform/files/overlay-dir.ts";
import { BlueprintReviewModal } from "./BlueprintReviewModal.tsx";

const filesViewProps = vi.hoisted(() => vi.fn());
const BLUEPRINT_PATH = overlayRel("blueprints", "2026", "test.md");

vi.mock("../../files/components/ProjectFilesView.tsx", () => ({
  ProjectFilesView: (props: unknown) => {
    filesViewProps(props);
    return <div data-testid="project-files-view" />;
  },
}));

type Overrides = Partial<Parameters<typeof BlueprintReviewModal>[0]>;

const appStore = {
  state: { messages: [], workflowRuns: [], sidecarStatus: "connected" },
  actions: { reportError: vi.fn() },
} as unknown as AppStore;

const roots = [
  {
    id: "root-1",
    path: "/repo",
    label: "repo",
    is_primary: true,
  },
] as Parameters<typeof BlueprintReviewModal>[0]["roots"];

function renderModal(overrides: Overrides = {}) {
  const onClose = vi.fn();
  const onOpenInFiles = vi.fn();
  const onDocumentSessionChange = vi.fn();
  const onApprove = vi.fn();
  const onChoiceTransition = vi.fn();
  const onRequestChanges = vi.fn();
  const result = render(() => (
    <BlueprintReviewModal
      reviewOpen
      initialView="preview"
      selectedBlueprintPath={BLUEPRINT_PATH}
      blueprintName="Test blueprint"
      projectId="project-1"
      appStore={appStore}
      roots={roots}
      error={null}
      warning={null}
      awaitingApproval
      canApprove
      changed={false}
      choiceTransitions={[]}
      onClose={onClose}
      onOpenInFiles={onOpenInFiles}
      onDocumentSessionChange={onDocumentSessionChange}
      onApprove={onApprove}
      onChoiceTransition={onChoiceTransition}
      onRequestChanges={onRequestChanges}
      {...overrides}
    />
  ));
  return {
    result,
    onClose,
    onOpenInFiles,
    onDocumentSessionChange,
    onApprove,
    onChoiceTransition,
    onRequestChanges,
  };
}

describe("BlueprintReviewModal", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
    filesViewProps.mockClear();
  });

  it("hosts the bound blueprint in a single-document Files surface", () => {
    renderModal();
    expect(screen.getByTestId("project-files-view")).toBeTruthy();
    expect(filesViewProps).toHaveBeenCalledOnce();
    expect(filesViewProps.mock.calls[0]?.[0]).toMatchObject({
      projectId: "project-1",
      document: {
        rootId: "root-1",
        path: BLUEPRINT_PATH,
        initialMarkdownView: "preview",
      },
    });
    const props = filesViewProps.mock.calls[0]?.[0] as {
      document: { previewSource: (source: string) => string };
    };
    expect(
      props.document.previewSource("---\nstatus: draft\n---\n# Goal"),
    ).toBe("# Goal");
  });

  it("requires a bound blueprint file", () => {
    renderModal({ selectedBlueprintPath: null });
    expect(screen.queryByTestId("blueprint-review-modal")).toBeNull();
    expect(filesViewProps).not.toHaveBeenCalled();
  });

  it("opens card-initiated editing in the Files code view", () => {
    renderModal({ initialView: "code" });
    expect(filesViewProps.mock.calls[0]?.[0]).toMatchObject({
      document: { initialMarkdownView: "code" },
    });
  });

  it("transfers to Files from the header", () => {
    const { onOpenInFiles } = renderModal();
    screen.getByTestId("blueprint-review-open-files").click();
    expect(onOpenInFiles).toHaveBeenCalledOnce();
  });

  it("approve remains the primary footer action", () => {
    const { onApprove } = renderModal();
    expect(
      screen.getByText(
        "Approving accepts this blueprint and continues the workflow.",
      ),
    ).toBeTruthy();
    const button = screen.getByTestId(
      "blueprint-review-approve",
    ) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    button.click();
    expect(onApprove).toHaveBeenCalledOnce();
  });

  it("hides approval chrome when the workflow is not awaiting", () => {
    renderModal({ awaitingApproval: false, canApprove: false });
    expect(screen.queryByTestId("blueprint-review-approve")).toBeNull();
    expect(screen.queryByTestId("blueprint-review-request-changes")).toBeNull();
    expect(screen.getByTestId("blueprint-review-done")).toBeTruthy();
  });

  it("renders host choice arms and fires the matching transition", () => {
    const { onChoiceTransition } = renderModal({
      choiceTransitions: [
        { id: "critique", label: "Run critique", armed: true },
        { id: "deepen", label: "Deepen research", armed: false },
      ],
    });
    screen.getByTestId("blueprint-review-choice-critique").click();
    expect(onChoiceTransition).toHaveBeenCalledWith("critique");
    expect(
      (screen.getByTestId(
        "blueprint-review-choice-deepen",
      ) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("sends request-changes text back to chat", async () => {
    const { onRequestChanges } = renderModal();
    screen.getByTestId("blueprint-review-request-changes").click();
    const input = (await screen.findByTestId(
      "blueprint-review-changes-input",
    )) as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Tighten the rollout" } });
    screen.getByTestId("blueprint-review-changes-send").click();
    await waitFor(() =>
      expect(onRequestChanges).toHaveBeenCalledWith("Tighten the rollout"),
    );
  });

  it("keeps request text when the Files draft cannot be saved", async () => {
    renderModal({ onRequestChanges: vi.fn(async () => false) });
    screen.getByTestId("blueprint-review-request-changes").click();
    const input = (await screen.findByTestId(
      "blueprint-review-changes-input",
    )) as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Do not lose this" } });
    screen.getByTestId("blueprint-review-changes-send").click();
    await waitFor(() => expect(input.value).toBe("Do not lose this"));
    expect(screen.getByTestId("blueprint-review-changes-form")).toBeTruthy();
  });

  it("shows the changed lead when the blueprint moved under approval", () => {
    renderModal({ changed: true });
    expect(screen.getByTestId("blueprint-review-lead").textContent).toContain(
      "approve this revision",
    );
  });
});
