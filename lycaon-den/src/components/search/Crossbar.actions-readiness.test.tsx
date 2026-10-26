import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@solidjs/testing-library";
import type { ContributionFrameResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { crossbarCommands } from "../../contributions/dispatch.ts";
import {
  contributionFrameHydrating, ensureContributionFrame, initContributionStore,
  invalidateContributionFrame, resetContributionStoreForTest,
  seedContributionFrameForTest, setContributionRetrySleepForTest,
} from "../../contributions/contribution-store.ts";
import { STOCK_FRAME } from "../../contributions/stock-frame.generated.ts";
import { ALL_SHELL_FACTS } from "../../contributions/stock-frame-test.ts";
import * as facts from "../../contributions/shell-facts.ts";
import { resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";
import { Crossbar } from "./Crossbar.tsx";

vi.mock("../../platform/connection/app-connection.ts", async (original) => ({
  ...await original<typeof import("../../platform/connection/app-connection.ts")>(),
  getLycaonClient: () => null,
}));

const commandId = "painted-wolf/platform:settings-section-providers";
const query = "> Settings: AI providers";
function present() {
  return render(() => <Crossbar open originProjectId="fixed-project" seed={query}
    initialMode="actions" contributionActions={crossbarCommands()}
    onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
}

afterEach(() => {
  cleanup();
  resetContributionStoreForTest();
  resetDispatcherForTests();
  vi.restoreAllMocks();
  localStorage.clear();
});

it("presents first-frame hydration then the actual stock Actions row from the same snapshot", async () => {
  resetContributionStoreForTest();
  facts.setShellFactState(ALL_SHELL_FACTS);
  let resolve!: (frame: ContributionFrameResponse) => void;
  const pending = new Promise<ContributionFrameResponse>((release) => { resolve = release; });
  const client = stubClient({ getContributions: vi.fn(() => pending) });
  initContributionStore(() => client);
  present();
  expect(contributionFrameHydrating()).toBe(true);
  expect(screen.getByText("Actions are loading…").getAttribute("role")).toBe("status");
  expect(screen.queryByText("No matching actions or results.")).toBeNull();
  resolve(STOCK_FRAME);
  await ensureContributionFrame();
  expect(await screen.findByTestId(`crossbar-action-${commandId}`)).toBeTruthy();
  expect(screen.queryByText("Actions are loading…")).toBeNull();
  expect(screen.queryByText("Actions unavailable")).toBeNull();
});

it("distinguishes unavailable hydration from no match and retains a ready frame during a failed refresh", async () => {
  resetContributionStoreForTest();
  facts.setShellFactState(ALL_SHELL_FACTS);
  setContributionRetrySleepForTest(() => Promise.resolve());
  let fail = true;
  const client = stubClient({ getContributions: vi.fn(async () => {
    if (fail) throw new Error("unavailable");
    return STOCK_FRAME;
  }) });
  initContributionStore(() => client);
  present();
  await ensureContributionFrame();
  expect(screen.getByText("Actions unavailable").getAttribute("role")).toBe("status");
  expect(screen.queryByText("No matching actions or results.")).toBeNull();
  fail = false;
  await invalidateContributionFrame();
  await screen.findByTestId(`crossbar-action-${commandId}`);
  fail = true;
  const refreshing = invalidateContributionFrame();
  expect(screen.queryByText("Actions are loading…")).toBeNull();
  expect(screen.getByTestId(`crossbar-action-${commandId}`)).toBeTruthy();
  await refreshing;
  expect(screen.getByTestId(`crossbar-action-${commandId}`)).toBeTruthy();
  expect(screen.queryByText("Actions unavailable")).toBeNull();
});

it("keeps ready filtered emptiness distinct from unavailable shell facts", async () => {
  resetContributionStoreForTest();
  const command = STOCK_FRAME.commands.find((row) => row.id === commandId)!;
  seedContributionFrameForTest({ ...STOCK_FRAME, commands: [{ ...command, when: { fact: "editor_active" } }] });
  facts.setShellFactState({ ...ALL_SHELL_FACTS, editor: null });
  const view = present();
  expect(crossbarCommands()).toEqual({ phase: "ready", commands: [] });
  expect(screen.queryByText("Actions are loading…")).toBeNull();
  expect(screen.queryByText("Actions unavailable")).toBeNull();
  view.unmount();
  vi.spyOn(facts, "liveShellFactLookup").mockReturnValue(null);
  present();
  expect(crossbarCommands()).toEqual({ phase: "unavailable", commands: [] });
  expect(screen.getByText("Actions unavailable")).toBeTruthy();
  expect(screen.queryByTestId(`crossbar-action-${commandId}`)).toBeNull();
});

it("does not present Actions availability in a Files query", async () => {
  resetContributionStoreForTest();
  render(() => <Crossbar open originProjectId="fixed-project" seed="file-name" initialMode="files"
    contributionActions={crossbarCommands()} onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
  await waitFor(() => expect(screen.queryByText("Actions unavailable")).toBeNull());
  expect(screen.queryByText("Actions are loading…")).toBeNull();
});
