import { stubClient } from "../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  ContributionChoiceResponse,
  ContributionCommand,
  ContributionFrameResponse,
  ContributionInteractionStep,
} from "../../api/types.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "../../contributions/contribution-store.ts";
import { ContributionCommandFlow } from "./ContributionCommandFlow.tsx";
import { registerCommandContextProvider } from "../../contributions/dispatch.ts";

const clipboard = vi.hoisted(() => ({ copy: vi.fn(async (_text: string) => undefined) }));
vi.mock("../../utils/clipboard.ts", () => ({ copyTextToClipboard: clipboard.copy }));

function frame(command: ContributionCommand): ContributionFrameResponse {
  return {
    frame_revision: "frame-1",
    commands: [command],
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    themes: [],
    configuration: [],
    requirements: [],
    search_sources: [],
    operations: [],
    notes: [],
  };
}

function inputCommand(): ContributionCommand {
  return {
    id: "acme/issues:create",
    provider: "acme/issues",
    title: "Create issue",
    category: "Issues",
    icon: "tool",
    executor: "host",
    invocation: "project",
    action_kind: "mcp_tool",
    result_treatment: "output",
    input: [
      { id: "title", title: "Title", type: "string", required: true },
      { id: "priority", title: "Priority", type: "enum", values: ["low", "high"], default: "low" },
    ],
  };
}

afterEach(() => {
  clipboard.copy.mockClear();
  cleanup();
  resetContributionStoreForTest();
});

describe("ContributionCommandFlow", () => {
  it("retains the chosen editor target while a form is filled", async () => {
    const command = inputCommand();
    seedContributionFrameForTest(frame(command));
    let target = { root_id: "root", path: "chosen.ts", document_revision: 4 };
    const release = registerCommandContextProvider(() => target);
    const invokeProjectCommand = vi.fn(async () => ({ status: "completed" as const, frame_revision: "frame-1" }));
    try {
      render(() => <ContributionCommandFlow command={command}
        deps={{ client: stubClient({ invokeProjectCommand }), projectId: "p1", sessionId: null }}
        onBack={vi.fn()} onClose={vi.fn()} />);
      target = { ...target, path: "other.ts", document_revision: 5 };
      fireEvent.input(screen.getByLabelText("Title *"), { target: { value: "Review" } });
      fireEvent.click(screen.getByRole("button", { name: "Run" }));
      await waitFor(() => expect(invokeProjectCommand).toHaveBeenCalledOnce());
      expect(invokeProjectCommand).toHaveBeenCalledWith("p1", command.id,
        expect.objectContaining({ context: { root_id: "root", path: "chosen.ts", document_revision: 4 } }));
    } finally { release(); }
  });
  it.each([
    ["string", "textbox"], ["project_path", "textbox"], ["number", "spinbutton"],
    ["boolean", "checkbox"], ["confirmation", "checkbox"], ["choice", "radiogroup"],
  ] as const)("names the %s interaction control from its step title", (kind, role) => {
    const command = inputCommand();
    command.input = [];
    const step: ContributionInteractionStep = {
      id: "answer", title: "Orchard detail", kind, required: true,
      ...(kind === "choice" ? { values: [{ id: "one", label: "One" }] } : {}),
    };
    command.interaction = { steps: [step] };
    seedContributionFrameForTest(frame(command));
    render(() => <ContributionCommandFlow
      command={command}
      deps={{ client: stubClient({}), projectId: "p1", sessionId: null }}
      onBack={vi.fn()} onClose={vi.fn()}
    />);
    expect(screen.getByRole(role, { name: step.title })).toBeTruthy();
  });

  it("collects ordered fields once and presents untrusted output as text", async () => {
    const command = inputCommand();
    seedContributionFrameForTest(frame(command));
    const invokeProjectCommand = vi.fn(async () => ({
      status: "completed" as const,
      frame_revision: "frame-1",
      output: `<img src=x onerror="alert(1)">`,
    }));
    const onBack = vi.fn();
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: stubClient({ invokeProjectCommand }), projectId: "p1", sessionId: null }}
        onBack={onBack}
        onClose={vi.fn()}
      />
    ));

    const title = screen.getByLabelText("Title *") as HTMLInputElement;
    await waitFor(() => expect(document.activeElement).toBe(title));
    fireEvent.input(title, { target: { value: "Ship it" } });
    fireEvent.click(screen.getByLabelText("Priority"));
    fireEvent.click(screen.getByRole("option", { name: "high" }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));

    await waitFor(() => expect(invokeProjectCommand).toHaveBeenCalledOnce());
    expect(invokeProjectCommand).toHaveBeenCalledWith(
      "p1",
      command.id,
      expect.objectContaining({ args: { title: "Ship it", priority: "high" } }),
    );
    const output = await screen.findByTestId("contribution-command-output");
    expect(output.textContent).toContain("<img src=x");
    expect(output.querySelector("img")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(clipboard.copy).toHaveBeenCalledWith(`<img src=x onerror="alert(1)">`);
    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(onBack).toHaveBeenCalledOnce();
    expect(invokeProjectCommand).toHaveBeenCalledOnce();
  });

  it("backs out before submit without creating an invocation", () => {
    const command = inputCommand();
    seedContributionFrameForTest(frame(command));
    const invokeProjectCommand = vi.fn();
    const onBack = vi.fn();
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: stubClient({ invokeProjectCommand }), projectId: "p1", sessionId: null }}
        onBack={onBack}
        onClose={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByLabelText("Back to results"));
    expect(onBack).toHaveBeenCalledOnce();
    expect(invokeProjectCommand).not.toHaveBeenCalled();
  });

  it("loads a dynamic picker explicitly, preserves it on Back, and requires confirmation", async () => {
    const command: ContributionCommand = {
      ...inputCommand(),
      input: [],
      result_treatment: "discard",
      interaction: {
        steps: [
          {
            id: "repository",
            title: "Repository",
            kind: "choice",
            required: true,
            source: { requirement: "acme/issues:tracker", tool: "list", inputs: {} },
          },
          { id: "confirm", title: "Create it?", kind: "confirmation", required: true },
        ],
      },
    };
    seedContributionFrameForTest(frame(command));
    const resolveContributionChoices = vi.fn(async () => ({
      frame_revision: "frame-1",
      provider: "tracker",
      choices: [{ id: "repo-1", label: "Repository one" }],
    }));
    const invokeProjectCommand = vi.fn(async () => ({ status: "completed" as const, frame_revision: "frame-1" }));
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: stubClient({ resolveContributionChoices, invokeProjectCommand }), projectId: "p1", sessionId: null }}
        onBack={vi.fn()}
        onClose={vi.fn()}
      />
    ));

    expect(resolveContributionChoices).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Load choices" }));
    const choice = await screen.findByLabelText("Repository one");
    fireEvent.click(choice);
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    const backButtons = screen.getAllByRole("button", { name: "Back" });
    fireEvent.click(backButtons[backButtons.length - 1]!);
    expect((screen.getByLabelText("Repository one") as HTMLInputElement).checked).toBe(true);
    expect(resolveContributionChoices).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    expect((await screen.findByRole("alert")).textContent).toContain("must be confirmed");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await waitFor(() => expect(invokeProjectCommand).toHaveBeenCalledOnce());
    expect(invokeProjectCommand).toHaveBeenCalledWith(
      "p1",
      command.id,
      expect.objectContaining({ args: { repository: "repo-1", confirm: true } }),
    );
    expect(resolveContributionChoices).toHaveBeenCalledOnce();
  });

  it("cancels dependency-stale choice requests and ignores their responses", async () => {
    const command: ContributionCommand = {
      ...inputCommand(),
      input: [],
      result_treatment: "discard",
      interaction: {
        steps: [
          {
            id: "organization",
            title: "Organization",
            kind: "choice",
            required: true,
            values: [{ id: "a", label: "Organization A" }, { id: "b", label: "Organization B" }],
          },
          {
            id: "repository",
            title: "Repository",
            kind: "choice",
            required: true,
            source: { requirement: "acme/issues:tracker", tool: "list", inputs: { organization: "organization" } },
          },
        ],
      },
    };
    seedContributionFrameForTest(frame(command));
    let resolveFirst!: (value: ContributionChoiceResponse) => void;
    let resolveSecond!: (value: ContributionChoiceResponse) => void;
    const first = new Promise<ContributionChoiceResponse>((resolve) => { resolveFirst = resolve; });
    const second = new Promise<ContributionChoiceResponse>((resolve) => { resolveSecond = resolve; });
    const resolveContributionChoices = vi.fn()
      .mockReturnValueOnce(first)
      .mockReturnValueOnce(second);
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: stubClient({ resolveContributionChoices }), projectId: "p1", sessionId: null }}
        onBack={vi.fn()}
        onClose={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByLabelText("Organization A"));
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    fireEvent.click(screen.getByRole("button", { name: "Load choices" }));
    const firstSignal = resolveContributionChoices.mock.calls[0]?.[4] as AbortSignal;
    const backButtons = screen.getAllByRole("button", { name: "Back" });
    fireEvent.click(backButtons[backButtons.length - 1]!);
    fireEvent.click(screen.getByLabelText("Organization B"));
    expect(firstSignal.aborted).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    fireEvent.click(screen.getByRole("button", { name: "Load choices" }));

    resolveFirst({ frame_revision: "frame-1", provider: "tracker", choices: [{ id: "old", label: "Old repository" }] });
    await Promise.resolve();
    expect(screen.queryByLabelText("Old repository")).toBeNull();
    resolveSecond({ frame_revision: "frame-1", provider: "tracker", choices: [{ id: "new", label: "New repository" }] });
    expect(await screen.findByLabelText("New repository")).toBeTruthy();
  });
});

describe("input field values", () => {
  it("string_list keeps the newline the user just typed", async () => {
    const command: ContributionCommand = {
      ...inputCommand(),
      input: [{ id: "labels", title: "Labels", type: "string_list", required: false }],
    };
    seedContributionFrameForTest(frame(command));
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: null, projectId: "p1", sessionId: null }}
        onBack={vi.fn()}
        onClose={vi.fn()}
      />
    ));
    const area = document.querySelector("textarea") as HTMLTextAreaElement;
    expect(area).toBeTruthy();
    // Type an entry, press Enter (newline lands in the raw text), keep typing.
    fireEvent.input(area, { target: { value: "first\n" } });
    expect(area.value).toBe("first\n");
    fireEvent.input(area, { target: { value: "first\nsecond" } });
    expect(area.value).toBe("first\nsecond");
  });

  it("a cleared number field is empty, not NaN", async () => {
    const command: ContributionCommand = {
      ...inputCommand(),
      input: [{ id: "count", title: "Count", type: "number", required: true }],
    };
    seedContributionFrameForTest(frame(command));
    const invokeProjectCommand = vi.fn(async () => ({
      status: "completed" as const,
      frame_revision: "frame-1",
    }));
    render(() => (
      <ContributionCommandFlow
        command={command}
        deps={{ client: stubClient({ invokeProjectCommand }), projectId: "p1", sessionId: null }}
        onBack={vi.fn()}
        onClose={vi.fn()}
      />
    ));
    const field = document.querySelector('input[type="number"]') as HTMLInputElement;
    expect(field).toBeTruthy();
    fireEvent.input(field, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Run" }));
    await screen.findByRole("alert");
    expect(invokeProjectCommand).not.toHaveBeenCalled();
  });
});
