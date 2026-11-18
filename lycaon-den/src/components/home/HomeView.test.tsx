import { assertNamedControl } from "../../test/controls.ts";
import { afterEach, beforeEach, describe, expect, it, vi, onTestFinished } from "vitest";
import { applyWebEngineFact } from "../../platform/desktop/webkit-atspi-workaround.ts";
import { loadFailed, loaded } from "../../store/load-state.ts";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { ALL_PROJECTS_PAGE_SIZE } from "../../home/home-model.ts";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { syncChatPrefsFromSnapshot } from "../../settings/chat/chat-prefs.ts";
import { getAppStateSnapshot, resetAppStateSnapshotForTests, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import type { SessionMode } from "../../api/types.ts";
import type { HomeIdeaDraft } from "../../home/idea-attachments.ts";
import { HomeView } from "./HomeView.tsx";

beforeEach(() => {
  resetAppStateSnapshotForTests();
  syncChatPrefsFromSnapshot();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function reflectStartedMode(mode: SessionMode) {
  setAppStateSnapshot({ ...getAppStateSnapshot(), chat: { lastStartedMode: mode } });
  syncChatPrefsFromSnapshot();
}

function stubSummary(i: number): ProjectSummary {
  return {
    id: `p${i}`,
    displayName: `Project ${i}`,
    folders: [],
    primaryFolder: null,
    folderLabel: "No folder",
    sessionCount: 0,
    chatCountLabel: "0 chats",
    lastActivityLabel: "new",
    lastActivityAtMs: null,
    starred: false,
    isDraft: false,
    coverArtifactId: null,
    coverRootSessionId: null,
  };
}

const homeHandlers = {
  onSubmitIdea: () => true,
  onOpenFolder: () => undefined,
  onCloneRepo: () => undefined,
  onOpenProject: () => undefined,
  onToggleStar: () => undefined,
  onRename: () => undefined,
  onAttachFolder: () => undefined,
  onPromote: () => undefined,
  onDelete: () => undefined,
};

describe("HomeView missing provider config", () => {
  it("renders the shared provider card and invokes the handler", () => {
    const onOpenProviders = vi.fn();
    const { getByTestId, queryByTestId } = render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        providerGap="no_provider"
        onOpenProviders={onOpenProviders}
        {...homeHandlers}
      />
    ));
    expect(getByTestId("no-provider-banner")).toBeTruthy();
    fireEvent.click(getByTestId("no-provider-open-settings"));
    expect(onOpenProviders).toHaveBeenCalledTimes(1);
    // Teaching CTA remains independent of the card.
    expect(queryByTestId("home-teaching")).toBeTruthy();
  });

  // The two halves of the config need different instructions.
  it("asks for a model, not a provider, when a provider is already ready", () => {
    const { getByTestId } = render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        providerGap="no_default_model"
        onOpenProviders={() => undefined}
        {...homeHandlers}
      />
    ));
    expect(getByTestId("no-provider-title-model")).toBeTruthy();
  });

  it("omits the card when config is complete", () => {
    const { queryByTestId } = render(() => (
      <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />
    ));
    expect(queryByTestId("no-provider-banner")).toBeNull();
  });
});

describe("HomeView draft materialization", () => {
  it("submits and keeps Ask, and an untouched Home uses it on its next show", async () => {
    const onSubmitIdea = vi.fn((draft: HomeIdeaDraft) => {
      reflectStartedMode(draft.mode);
      return true;
    });
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    render(() => <ResidentPresenceProvider presence={presence()}>
      <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} onSubmitIdea={onSubmitIdea} />
    </ResidentPresenceProvider>);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    const ask = screen.getByRole("radio", { name: "Ask" });
    fireEvent.click(ask);
    fireEvent.input(input, { target: { value: "First question" } });
    fireEvent.click(screen.getByTestId("home-idea-send"));
    await vi.waitFor(() => expect(input.value).toBe(""));
    expect(ask.getAttribute("aria-checked")).toBe("true");
    fireEvent.input(input, { target: { value: "Second question" } });
    fireEvent.click(screen.getByTestId("home-idea-send"));
    await vi.waitFor(() => expect(input.value).toBe(""));
    expect(onSubmitIdea.mock.calls.map(([draft]) => draft.mode)).toEqual(["ask", "ask"]);

    setPresence("idle");
    reflectStartedMode("agent");
    setPresence("active");
    expect(screen.getByRole("radio", { name: "Agent" }).getAttribute("aria-checked")).toBe("true");
  });

  it("retains a typed Home draft across preference changes and a rejected send", async () => {
    reflectStartedMode("ask");
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    const [submitting, setSubmitting] = createSignal(false);
    const onSubmitIdea = vi.fn((_draft: HomeIdeaDraft) => false);
    render(() => <ResidentPresenceProvider presence={presence()}>
      <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} submitting={submitting()} onSubmitIdea={onSubmitIdea} />
    </ResidentPresenceProvider>);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    const ask = screen.getByRole("radio", { name: "Ask" });
    expect(ask.getAttribute("aria-checked")).toBe("true");
    fireEvent.input(input, { target: { value: "Keep this draft" } });
    setPresence("idle");
    reflectStartedMode("agent");
    setPresence("active");

    setSubmitting(true);
    fireEvent.click(screen.getByRole("radio", { name: "Agent" }));
    expect(ask.getAttribute("aria-checked")).toBe("true");
    setSubmitting(false);
    fireEvent.click(screen.getByTestId("home-idea-send"));
    expect(onSubmitIdea).toHaveBeenCalledExactlyOnceWith({ text: "Keep this draft", mode: "ask", attachments: [] });
    await Promise.resolve();
    expect(input.value).toBe("Keep this draft");
    expect(ask.getAttribute("aria-checked")).toBe("true");

    fireEvent.input(input, { target: { value: "" } });
    expect(screen.getByRole("radio", { name: "Agent" }).getAttribute("aria-checked")).toBe("true");
  });

  it("keeps pointer focus and the input node while fading the empty hint", () => {
    vi.useFakeTimers();
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    const group = screen.getByRole("radiogroup", { name: "Session mode" });
    const ask = screen.getByRole("radio", { name: "Ask" });
    const agent = screen.getByRole("radio", { name: "Agent" });
    expect(group.nextElementSibling).toBe(screen.getByTestId("home-idea-send"));

    const press = (segment: HTMLElement) => {
      const event = new Event("pointerdown", { bubbles: true, cancelable: true });
      segment.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(true);
      fireEvent.click(segment);
    };

    input.focus();
    press(ask);
    expect(document.activeElement).toBe(input);
    expect(screen.getByTestId("home-idea-input")).toBe(input);
    expect(input.placeholder).toBe("Describe your idea…");
    expect(input.hasAttribute("data-hint-out")).toBe(true);
    vi.advanceTimersByTime(120);
    expect(input.placeholder).toBe("Ask a question…");
    expect(input.hasAttribute("data-hint-out")).toBe(false);

    input.blur();
    press(agent);
    expect(document.activeElement).toBe(document.body);
    fireEvent.input(input, { target: { value: "Keep the text" } });
    expect(input.placeholder).toBe("Describe your idea…");
    expect(input.value).toBe("Keep the text");

    agent.focus();
    fireEvent.keyDown(agent, { key: "ArrowRight" });
    expect(document.activeElement).toBe(ask);
    expect(input.placeholder).toBe("Ask a question…");
  });

  it("swaps the drawn hint immediately under reduced motion", () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true })));
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    onTestFinished(() => applyWebEngineFact(null));
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />);
    fireEvent.click(screen.getByRole("radio", { name: "Ask" }));
    const hint = document.querySelector(".home-view__idea-hint");
    expect(hint?.textContent).toBe("Ask a question…");
    expect(hint?.hasAttribute("data-hint-out")).toBe(false);
  });

  it("shows the idea hint as the field's placeholder without the WebKitGTK workaround", () => {
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} onSubmitIdea={vi.fn()} />);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    expect(input.getAttribute("placeholder")).toBe("Describe your idea…");
    expect(input.hasAttribute("aria-label")).toBe(false);
    expect(document.querySelector(".home-view__idea-hint")).toBeNull();
  });

  it("draws the idea hint while the field is empty under the WebKitGTK workaround", () => {
    applyWebEngineFact({ engine: "webkitgtk", version: "2.54.1" });
    onTestFinished(() => applyWebEngineFact(null));
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} onSubmitIdea={vi.fn()} />);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    expect(input.hasAttribute("placeholder")).toBe(false);
    expect(input.getAttribute("aria-label")).toBe("Describe your idea");
    expect(document.querySelector(".home-view__idea-hint")?.textContent).toBe("Describe your idea…");
    fireEvent.input(input, { target: { value: "An orchard notebook" } });
    expect(document.querySelector(".home-view__idea-hint")).toBeNull();
  });

  it.each([true, false])("clears only accepted Home drafts (accepted=%s)", async (accepted) => {
    let settle!: (accepted: boolean) => void;
    const result = new Promise<boolean>((resolve) => { settle = resolve; });
    const onSubmitIdea = vi.fn(() => result);
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} onSubmitIdea={onSubmitIdea} />);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "An orchard notebook" } });
    fireEvent.click(screen.getByTestId("home-idea-send"));
    expect(input.value).toBe("An orchard notebook");
    settle(accepted);
    await vi.waitFor(() => expect(input.value).toBe(accepted ? "" : "An orchard notebook"));
  });

  it("preserves a newer Home draft when an older submission settles", async () => {
    let settle!: (accepted: boolean) => void;
    const result = new Promise<boolean>((resolve) => { settle = resolve; });
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} onSubmitIdea={() => result} />);
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "First idea" } });
    fireEvent.click(screen.getByTestId("home-idea-send"));
    fireEvent.input(input, { target: { value: "Next idea" } });
    settle(true);
    await result;
    expect(input.value).toBe("Next idea");
  });

  it("keeps the submitted text and disables duplicate submission", () => {
    const [submitting, setSubmitting] = createSignal(false);
    const onSubmitIdea = vi.fn((draft: { text: string }) => {
      expect(draft.text).toBe("Build a release dashboard");
      setSubmitting(true);
      return false;
    });
    render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        {...homeHandlers}
        submitting={submitting()}
        onSubmitIdea={onSubmitIdea}
      />
    ));

    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Build a release dashboard" } });
    for (const composition of [{ isComposing: true }, { keyCode: 229 }]) {
      fireEvent.keyDown(input, { key: "Enter", ...composition });
      expect(onSubmitIdea).not.toHaveBeenCalled();
      expect(input.disabled).toBe(false);
    }
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onSubmitIdea).toHaveBeenCalledTimes(1);
    expect(input.value).toBe("Build a release dashboard");
    expect(input.disabled).toBe(true);
    expect(
      (screen.getByTestId("home-idea-send") as HTMLButtonElement).disabled,
    ).toBe(true);

    fireEvent.click(screen.getByTestId("home-idea-send"));
    expect(onSubmitIdea).toHaveBeenCalledTimes(1);
  });
});

describe("HomeView all projects list", () => {
  it("paginates when more than one page of projects", () => {
    const summaries = Array.from({ length: ALL_PROJECTS_PAGE_SIZE + 3 }, (_, i) => ({
      ...stubSummary(i),
      displayName: `Item ${String(i).padStart(2, "0")}`,
    }));
    const { getByTestId, queryByTestId } = render(() => (
      <HomeView summaries={summaries} section="all" registry={loaded(null)} {...homeHandlers} />
    ));
    expect(getByTestId("home-list")).toBeTruthy();
    expect(getByTestId("project-list-row-p0")).toBeTruthy();
    expect(queryByTestId(`project-list-row-p${ALL_PROJECTS_PAGE_SIZE}`)).toBeNull();
    expect(getByTestId("home-list-pager")).toBeTruthy();
    fireEvent.click(getByTestId("home-list-pager-next"));
    expect(getByTestId(`project-list-row-p${ALL_PROJECTS_PAGE_SIZE}`)).toBeTruthy();
    expect(queryByTestId("project-list-row-p0")).toBeNull();
  });

  it("hides the pager when everything fits on one page", () => {
    const summaries = [stubSummary(0), stubSummary(1)];
    const { getByTestId, queryByTestId } = render(() => (
      <HomeView summaries={summaries} section="all" registry={loaded(null)} {...homeHandlers} />
    ));
    expect(getByTestId("home-list")).toBeTruthy();
    expect(queryByTestId("home-list-pager")).toBeNull();
  });

  it("includes drafts in all projects with draft badge and promote/discard actions", () => {
    const onOpenProject = vi.fn(), onPromote = vi.fn(), onDelete = vi.fn();
    const summaries = [stubSummary(0), { ...stubSummary(1), id: "d1", displayName: "Draft Idea", isDraft: true }];
    const { getByTestId, queryByTestId } = render(() => (
      <HomeView summaries={summaries} section="all" registry={loaded(null)} {...homeHandlers} onOpenProject={onOpenProject} onPromote={onPromote} onDelete={onDelete} />
    ));
    expect(getByTestId("project-list-row-p0")).toBeTruthy();
    expect(getByTestId("project-list-row-d1")).toBeTruthy();
    expect(queryByTestId("project-list-draft-p0")).toBeNull();
    expect(getByTestId("project-list-draft-d1").textContent).toBe("Draft");
    fireEvent.click(screen.getByText("Draft Idea"));
    expect(onOpenProject).toHaveBeenCalledWith("d1");
    fireEvent.click(getByTestId("project-list-menu-d1"));
    fireEvent.click(assertNamedControl(screen.getByTestId("project-list-promote-d1"), "menuitem"));
    expect(onPromote).toHaveBeenCalledWith("d1");
    fireEvent.click(getByTestId("project-list-menu-d1"));
    const discardBtn = assertNamedControl(screen.getByTestId("project-list-delete-d1"), "menuitem");
    expect(discardBtn.textContent).toBe("Discard draft");
    fireEvent.click(discardBtn);
    expect(onDelete).toHaveBeenCalledWith("d1");
  });

  it("multi-selects and runs bulk star / delete", async () => {
    const onToggleStar = vi.fn();
    const onDelete = vi.fn();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const summaries = [stubSummary(0), stubSummary(1), stubSummary(2)];
    const { getByTestId, queryByTestId, findByTestId } = render(() => (
      <HomeView
        summaries={summaries}
        section="all"
        registry={loaded(null)}
        {...homeHandlers}
        onToggleStar={onToggleStar}
        onDelete={onDelete}
      />
    ));
    expect(queryByTestId("home-list-toolbar")).toBeNull();
    fireEvent.click(getByTestId("project-list-select-p0"));
    fireEvent.click(getByTestId("project-list-select-p2"));
    expect(await findByTestId("home-list-toolbar")).toBeTruthy();
    fireEvent.click(getByTestId("home-list-bulk-star"));
    expect(onToggleStar).toHaveBeenCalledWith("p0", true);
    expect(onToggleStar).toHaveBeenCalledWith("p2", true);
    fireEvent.click(getByTestId("home-list-overflow-trigger"));
    // Overflow menu portals to document.body — query via screen.
    fireEvent.click(await screen.findByTestId("home-list-bulk-delete"));
    await vi.waitFor(() => {
      expect(onDelete).toHaveBeenCalledWith("p0", { confirmed: true });
      expect(onDelete).toHaveBeenCalledWith("p2", { confirmed: true });
    });
    expect(onDelete).not.toHaveBeenCalledWith("p1");
    confirmSpy.mockRestore();
  });

  it("triggers deletion for a single project without preliminary modal", () => {
    const onDelete = vi.fn();
    const confirmSpy = vi.spyOn(window, "confirm");
    const summaries = [stubSummary(0)];
    const { getByTestId } = render(() => (
      <HomeView
        summaries={summaries}
        section="recents"
        registry={loaded(null)}
        {...homeHandlers}
        onDelete={onDelete}
      />
    ));
    fireEvent.click(getByTestId("project-card-menu-p0"));
    fireEvent.click(assertNamedControl(screen.getByTestId("project-card-delete-p0"), "menuitem"));
    expect(onDelete).toHaveBeenCalledWith("p0");
    expect(confirmSpy).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("sorts the list when column headers are clicked", () => {
    const summaries = [
      stubSummary(0),
      { ...stubSummary(1), displayName: "Alpha", id: "alpha" },
      { ...stubSummary(2), displayName: "Zulu", id: "zulu" },
    ];
    const { getByTestId, getAllByTestId } = render(() => (
      <HomeView summaries={summaries} section="all" registry={loaded(null)} {...homeHandlers} />
    ));
    fireEvent.click(getByTestId("home-list-sort-name"));
    let ids = getAllByTestId(/project-list-row-/).map((el) => el.getAttribute("data-testid"));
    expect(ids).toEqual([
      "project-list-row-alpha",
      "project-list-row-p0",
      "project-list-row-zulu",
    ]);
    fireEvent.click(getByTestId("home-list-sort-name"));
    ids = getAllByTestId(/project-list-row-/).map((el) => el.getAttribute("data-testid"));
    expect(ids).toEqual([
      "project-list-row-zulu",
      "project-list-row-p0",
      "project-list-row-alpha",
    ]);
  });

  it("selects the whole page from the header checkbox", async () => {
    const summaries = [stubSummary(0), stubSummary(1)];
    const { getByTestId, findByTestId } = render(() => (
      <HomeView summaries={summaries} section="all" registry={loaded(null)} {...homeHandlers} />
    ));
    fireEvent.click(getByTestId("home-list-select-page"));
    expect(await findByTestId("home-list-toolbar")).toBeTruthy();
    expect((getByTestId("project-list-select-p0") as HTMLInputElement).checked).toBe(true);
    expect((getByTestId("project-list-select-p1") as HTMLInputElement).checked).toBe(true);
  });

  it("never greets a user whose registry failed to load as brand new", () => {
    // Only loaded-empty signals first-run; a failed read shows the failure.
    render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loadFailed(new Error("sidecar unreachable"))}
        {...homeHandlers}
      />
    ));
    expect(screen.queryByTestId("home-teaching")).toBeNull();
    expect(screen.getByTestId("home-empty").textContent).toMatch(/couldn.t load/i);
  });

  it("shows first-run teaching only for a settled, empty registry", () => {
    render(() => <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />);
    expect(screen.getByTestId("home-teaching")).toBeTruthy();
  });

  it("shows section empty hint instead of first-run teaching on recents when only drafts exist", () => {
    const summaries = [{ ...stubSummary(1), id: "d1", isDraft: true }];
    render(() => <HomeView summaries={summaries} section="recents" registry={loaded(null)} {...homeHandlers} />);
    expect(screen.queryByTestId("home-teaching")).toBeNull();
    expect(screen.getByTestId("home-empty").textContent).toBe("No projects yet.");
  });
});


describe("HomeView project identity", () => {
  // The card (recents) and row (all) surfaces name their controls per project id.
  const renameTestId = (section: "recents" | "all") =>
    section === "recents" ? "project-card-rename-p1" : "project-list-rename-p1";
  const renameField = (section: "recents" | "all") =>
    assertNamedControl(screen.getByTestId(renameTestId(section)), "textbox");
  const projectMenuTrigger = (section: "recents" | "all") =>
    assertNamedControl(
      screen.getByTestId(
        section === "recents" ? "project-card-menu-p1" : "project-list-menu-p1",
      ),
      "button",
    );

  it.each(["recents", "all"] as const)("removes and recreates a project safely in %s", (section) => {
    const [summaries, setSummaries] = createSignal([stubSummary(1), stubSummary(2)]);
    render(() => <HomeView summaries={summaries()} section={section} registry={loaded(null)} {...homeHandlers} />);
    fireEvent.click(projectMenuTrigger(section));
    fireEvent.click(assertNamedControl(screen.getByTestId("rename"), "menuitem"));
    fireEvent.input(renameField(section), { target: { value: "Unsubmitted name" } });
    setSummaries([stubSummary(2)]);
    expect(screen.queryByTestId(renameTestId(section))).toBeNull();
    expect(screen.queryByText("Project 1")).toBeNull();
    setSummaries([{ ...stubSummary(1), displayName: "Recreated project" }, stubSummary(2)]);
    expect(screen.getByText("Recreated project")).toBeTruthy();
    expect(screen.queryByTestId(renameTestId(section))).toBeNull();
  });

  it.each(["recents", "all"] as const)("keeps an unfinished rename through %s refresh and reorder", (section) => {
    const [summaries, setSummaries] = createSignal([stubSummary(1), stubSummary(2)]);
    const onRename = vi.fn();
    render(() => <HomeView summaries={summaries()} section={section} registry={loaded(null)} {...homeHandlers} onRename={onRename} />);
    fireEvent.click(projectMenuTrigger(section));
    fireEvent.click(assertNamedControl(screen.getByTestId("rename"), "menuitem"));
    const input = renameField(section) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "Unsaved café rename" } });
    setSummaries([{ ...stubSummary(2), lastActivityAtMs: 999 }, { ...stubSummary(1), displayName: "Host updated name" }]);
    expect(renameField(section)).toBe(input);
    expect(input.value).toBe("Unsaved café rename");
    expect(onRename).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onRename).toHaveBeenCalledWith("p1", "Unsaved café rename");
  });
});
