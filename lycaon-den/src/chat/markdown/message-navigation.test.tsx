import { webcrypto } from "node:crypto";
import { createSignal } from "solid-js";
import { fireEvent, render, waitFor, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { MessageNavigationResponse, NavigationReference } from "../../api/types.ts";
import { AssistantProseBody } from "../../components/transcript/AssistantProseBody.tsx";
import { buildProseNavigationIndex } from "./prose-path-opens.ts";
import { navigationContentHash } from "./message-navigation.ts";
import { registerOpenSourceProjectLookup, registerOpenSourceSink, resetOpenSourceForTests } from "../../platform/navigation/open-source.ts";

const pending: NavigationReference = {
 id: "ref-0", syntax: "code", mention: "same.go", project_id: "p", path: "same.go", status: "pending", explicit: false };
afterEach(() => { vi.unstubAllGlobals(); resetOpenSourceForTests(); });

describe("message navigation", () => {
  it("lets the person select among full root-qualified candidates", async () => {
    vi.stubGlobal("crypto", webcrypto);
    const ref: NavigationReference = { ...pending, status: "ambiguous", candidates: [
      { project_id: "p", root_id: "r", path: "first/same.go", entry_kind: "file" },
      { project_id: "p", root_id: "r", path: "second/same.go", entry_kind: "file" },
    ] };
    const resolve = vi.fn(async (_sessionId, req) => ({ content_sha256: req.content_sha256, references: [req.candidate_index == null ? ref : {
      ...ref, ...ref.candidates![req.candidate_index], status: "resolved" as const, candidates: undefined,
    }] }));
    const client = stubClient({ resolveMessageNavigation: resolve });
    const open = vi.fn();
    registerOpenSourceProjectLookup(() => ({ roots: [{ id: "r", path: "/repo", is_primary: true }] }));
    registerOpenSourceSink(open);
    const view = render(() => <AssistantProseBody client={client} sessionId="s" messageId="m" projectId="p" source="See `same.go`." messageContent={"\nSee `same.go`.\n"} navigation={buildProseNavigationIndex([pending])} rootRefs={[{ id: "r", path: "/repo", label: "main", is_primary: true }]} />);
    await waitFor(() => expect(resolve).toHaveBeenCalled());
    expect(resolve.mock.calls[0]?.[1].content_sha256).toBe(await navigationContentHash("\nSee `same.go`.\n"));
    fireEvent.click(view.getByRole("button", { name: "same.go" }));
    await waitFor(() => expect(screen.getByRole("dialog", { name: "File navigation" })).toBeTruthy());
    fireEvent.click(await screen.findByRole("button", { name: "@main/second/same.go" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ rootId: "r", path: "second/same.go" })));
  });

  it("discards a result after its message content changes", async () => {
    vi.stubGlobal("crypto", webcrypto);
    let finish: (response: MessageNavigationResponse) => void = () => {};
    const resolve = vi.fn(() => new Promise<MessageNavigationResponse>((done) => { finish = done; }));
    const client = stubClient({ resolveMessageNavigation: resolve });
    const [source, setSource] = createSignal("See `same.go`.");
    const view = render(() => <AssistantProseBody client={client} sessionId="s" messageId="m" projectId="p" source={source()} navigation={buildProseNavigationIndex([pending])} />);
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(1));
    const complete = finish;
    setSource("Replaced answer.");
    complete({ content_sha256: await navigationContentHash("See `same.go`."), references: [{ ...pending, status: "resolved", root_id: "r", path: "old/same.go", entry_kind: "file" }] });
    await Promise.resolve();
    expect(view.container.textContent).toContain("Replaced answer.");
    expect(view.container.querySelector("[data-den-source-path]")).toBeNull();
  });
});

describe("contextual link presentation", () => {
  it("keeps unknown inferred paths plain without a lookup popup", async () => {
    vi.stubGlobal("crypto", webcrypto);
    const resolve = vi.fn(async (_sessionId, req) => ({ content_sha256: req.content_sha256, references: [{ ...pending, status: "missing" as const }] }));
    const client = stubClient({ resolveMessageNavigation: resolve });
    const view = render(() => <AssistantProseBody client={client} sessionId="s" messageId="plain" projectId="p" source="See `same.go`." navigation={buildProseNavigationIndex([pending])} />);
    expect(view.container.querySelector("button")).toBeNull();
    await waitFor(() => expect(resolve).toHaveBeenCalledTimes(1));
    expect(view.container.querySelector("button")).toBeNull();
    expect(screen.queryByRole("dialog", { name: "File navigation" })).toBeNull();
  });

  it("rechecks a known location before opening a path the host resolved as deleted", async () => {
    vi.stubGlobal("crypto", webcrypto);
    const resolve = vi.fn(async (_sessionId, req) => ({ content_sha256: req.content_sha256, references: [{ ...ref, deleted: true }] }));
    const client = stubClient({ resolveMessageNavigation: resolve });
    const open = vi.fn();
    registerOpenSourceProjectLookup(() => ({ roots: [{ id: "r", path: "/repo", is_primary: true }] }));
    registerOpenSourceSink(open);
    const ref: NavigationReference = { ...pending, status: "resolved", root_id: "r", path: ".ignored/same.go", entry_kind: "file" };
    const view = render(() => <AssistantProseBody client={client} sessionId="s" messageId="known" projectId="p" source="See `same.go`." navigation={buildProseNavigationIndex([ref])} />);
    expect(resolve).not.toHaveBeenCalled();
    fireEvent.click(view.getByRole("button", { name: "same.go" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith(expect.objectContaining({ rootId: "r", path: ".ignored/same.go" })));
    expect(resolve).toHaveBeenCalledWith("s", expect.objectContaining({ reference_id: "ref-0" }), expect.any(AbortSignal));
    expect(screen.queryByRole("dialog", { name: "File navigation" })).toBeNull();
  });
});
