import { render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatTabRail } from "./ChatTabRail.tsx";
import { ChatTabChromeProvider } from "../../chat/composer/chat-tab-chrome.tsx";
import { createPreparation } from "../../ui/presentation.ts";
import { PresentationProvider } from "../../ui/presentation-context.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { gitStatusRefreshPending, refreshGitStatus } from "../../chat/actions/git-actions.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { GitRepoEntry } from "../../api/types.ts";
import type { GitWorkspaceStatus } from "../../chat/actions/git-workspace-status.ts";
import { gitStatusReads } from "../../test/git-status-fixture.ts";

const repo: GitRepoEntry = {
  repo_id: "repo-1",
  label: "app",
  root_ids: ["r1"],
  available: true,
  ahead: 0,
  behind: 0,
  dirty: true,
  staged_count: 2,
  unstaged_count: 1,
  changed_count: 3,
};

function status(overrides: Partial<GitWorkspaceStatus> = {}): GitWorkspaceStatus {
  return {
    available: true,
    repo_id: "repo-1",
    root_ids: ["r1"],
    ahead: 0,
    behind: 0,
    dirty: false,
    staged_count: 0,
    unstaged_count: 0,
    changed_count: 0,
    files: [],
    ...overrides,
  };
}

function mountRail(appStore = createAppStore()) {
  const preparation = createPreparation();
  render(() => (
    <PresentationProvider preparation={preparation}>
      <ChatTabChromeProvider sessionKey="s1">
        <ChatTabRail appStore={appStore} sessionId="s1" onOpenWorker={() => {}} />
      </ChatTabChromeProvider>
    </PresentationProvider>
  ));
  const chip = screen.getByTestId("tab-git");
  return { appStore, preparation, chip };
}

const dimmed = (chip: HTMLElement) => chip.classList.contains("tabs__chip--empty");
const badge = (chip: HTMLElement) => chip.querySelector(".tabs__chip-badge")?.textContent ?? null;

describe("ChatTabRail git status", () => {
  afterEach(() => vi.restoreAllMocks());

  it("keeps the Git tab undimmed and the surface free before any status read starts", () => {
    const { preparation, chip } = mountRail();
    expect(dimmed(chip)).toBe(false);
    expect(badge(chip)).toBeNull();
    expect(preparation.pending()).not.toContain("git-status");
  });

  it("holds the surface across the first status read and lands the badge with it", async () => {
    const { appStore, preparation, chip } = mountRail();
    let releaseRepos!: (view: { repos: GitRepoEntry[]; active_repo_id: string }) => void;
    const client = stubClient({
      listGitRepos: vi.fn(() => new Promise<{ repos: GitRepoEntry[]; active_repo_id: string }>((resolve) => {
        releaseRepos = resolve;
      })),
      ...gitStatusReads(() => status({ dirty: true, staged_count: 2, unstaged_count: 1, changed_count: 3 })),
    });

    const flight = refreshGitStatus(appStore, client, "p1", "s1");
    expect(gitStatusRefreshPending(appStore)).toBe(true);
    expect(preparation.pending()).toContain("git-status");
    // The unresolved chip neither dims nor claims a count.
    expect(dimmed(chip)).toBe(false);
    expect(badge(chip)).toBeNull();

    // The read starts on a microtask; hold the answer until it has asked.
    await waitFor(() => expect(client.listGitRepos).toHaveBeenCalled());
    releaseRepos({ repos: [repo], active_repo_id: "repo-1" });
    await flight;
    expect(gitStatusRefreshPending(appStore)).toBe(false);
    await waitFor(() => expect(badge(chip)).toBe("3"));
    expect(dimmed(chip)).toBe(false);
    expect(preparation.pending()).not.toContain("git-status");
  });

  it("dims the Git tab only once a resolved read reports no changes", () => {
    const { appStore, preparation, chip } = mountRail();
    appStore.actions.setGitStatus(status());
    expect(dimmed(chip)).toBe(true);
    expect(badge(chip)).toBeNull();
    expect(preparation.pending()).not.toContain("git-status");
  });

  it("settles the surface when the status read fails", async () => {
    const { appStore, preparation, chip } = mountRail();
    const client = stubClient({
      listGitRepos: vi.fn(async () => {
        throw new Error("git unavailable");
      }),
    });
    const flight = refreshGitStatus(appStore, client, "p1", "s1");
    expect(preparation.pending()).toContain("git-status");
    await flight;
    expect(gitStatusRefreshPending(appStore)).toBe(false);
    expect(appStore.state.gitStatus.state).toBe("error");
    expect(preparation.pending()).not.toContain("git-status");
    // A failed read is a settled answer of no changes to show.
    expect(dimmed(chip)).toBe(true);
  });
});
