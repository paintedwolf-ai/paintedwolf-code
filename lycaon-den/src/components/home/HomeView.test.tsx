import { afterEach, describe, expect, it, vi } from "vitest";
import { loadFailed, loaded } from "../../store/load-state.ts";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { ALL_PROJECTS_PAGE_SIZE } from "../../home/home-model.ts";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { HomeView } from "./HomeView.tsx";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";

afterEach(() => {
  cleanup();
});

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
    const { getByText } = render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        providerGap="no_default_model"
        onOpenProviders={() => undefined}
        {...homeHandlers}
      />
    ));
    expect(getByText(MODELS_SETTINGS_COPY.noDefaultModelTitle)).toBeTruthy();
  });

  it("omits the card when config is complete", () => {
    const { queryByTestId } = render(() => (
      <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />
    ));
    expect(queryByTestId("no-provider-banner")).toBeNull();
  });
});

describe("HomeView draft materialization", () => {
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
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onSubmitIdea).toHaveBeenCalledTimes(1);
    expect(input.value).toBe("Build a release dashboard");
    expect(input.disabled).toBe(true);
    expect(
      (screen.getByTestId("home-idea-send") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.getByRole("status").textContent).toContain("Starting chat");

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
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
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
    render(() => (
      <HomeView
        summaries={[]}
        section="recents"
        registry={loaded(null)}
        {...homeHandlers}
      />
    ));
    expect(screen.getByTestId("home-teaching")).toBeTruthy();
  });
});


describe("HomeView project identity", () => {
  it.each(["recents", "all"] as const)("removes and recreates a project safely in %s", (section) => {
    const [summaries, setSummaries] = createSignal([stubSummary(1), stubSummary(2)]);
    render(() => <HomeView summaries={summaries()} section={section} registry={loaded(null)} {...homeHandlers} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Project actions" })[0]!);
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.input(screen.getByRole("textbox", { name: "Rename" }), { target: { value: "Unsubmitted name" } });
    setSummaries([stubSummary(2)]);
    expect(screen.queryByRole("textbox", { name: "Rename" })).toBeNull();
    expect(screen.queryByText("Project 1")).toBeNull();
    setSummaries([{ ...stubSummary(1), displayName: "Recreated project" }, stubSummary(2)]);
    expect(screen.getByText("Recreated project")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "Rename" })).toBeNull();
  });

  it.each(["recents", "all"] as const)("keeps an unfinished rename through %s refresh and reorder", (section) => {
    const [summaries, setSummaries] = createSignal([stubSummary(1), stubSummary(2)]);
    const onRename = vi.fn();
    render(() => <HomeView summaries={summaries()} section={section} registry={loaded(null)} {...homeHandlers} onRename={onRename} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Project actions" })[0]!);
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    const input = screen.getByRole("textbox", { name: "Rename" }) as HTMLInputElement;
    fireEvent.input(input, { target: { value: "Unsaved café rename" } });
    setSummaries([{ ...stubSummary(2), lastActivityAtMs: 999 }, { ...stubSummary(1), displayName: "Host updated name" }]);
    expect(screen.getByRole("textbox", { name: "Rename" })).toBe(input);
    expect(input.value).toBe("Unsaved café rename");
    expect(onRename).not.toHaveBeenCalled();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onRename).toHaveBeenCalledWith("p1", "Unsaved café rename");
  });
});
