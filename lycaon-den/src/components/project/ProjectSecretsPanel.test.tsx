import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { ProjectSecretsPanel } from "./ProjectSecretsPanel.tsx";

const frames = new Map<number, FrameRequestCallback>();
let frameId = 0;
function paint() {
  for (let i = 0; i < 4; i++) {
    const callbacks = [...frames.values()];
    frames.clear();
    for (const callback of callbacks) callback(performance.now());
  }
}
beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    frames.set(++frameId, callback);
    return frameId;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
});
afterEach(() => { cleanup(); frames.clear(); vi.unstubAllGlobals(); });

it("keeps the outgoing view until ignored values are ready and retains search on return", async () => {
  let resolveIgnores!: (value: { rules: [] }) => void;
  const listProjectSecretIgnores = vi.fn(() => new Promise<{ rules: [] }>((resolve) => { resolveIgnores = resolve; }));
  const client = stubClient({
    getProject: vi.fn(async () => ({ roots: [] })),
    listProjectManagedSecrets: vi.fn(async () => ({ secrets: [], count: 0 })),
    listProjectSecretIgnores,
  });
  render(() => <ProjectSecretsPanel client={client} projectId="project" />);
  const surface = (key: string) => screen.getAllByTestId("resident-surface").find((node) => node.dataset.residentKey === key)!;
  await vi.waitFor(() => { paint(); expect(surface("managed").dataset.resident).toBe("active"); });
  fireEvent.click(screen.getByRole("button", { name: "Ignored values" }));
  paint();
  expect(surface("managed").dataset.resident).toBe("active");
  expect(surface("ignored").getAttribute("aria-hidden")).toBe("true");
  // Projection reads start on the next microtask, after the incoming surface mounts.
  await vi.waitFor(() => expect(listProjectSecretIgnores).toHaveBeenCalledOnce());
  paint();
  expect(surface("managed").dataset.resident).toBe("active");
  resolveIgnores({ rules: [] });
  await vi.waitFor(() => { paint(); expect(surface("ignored").dataset.resident).toBe("active"); });
  const search = screen.getByLabelText("Search ignored values") as HTMLInputElement;
  fireEvent.input(search, { target: { value: "fixture" } });
  fireEvent.click(screen.getByRole("button", { name: "Managed secrets" }));
  paint();
  fireEvent.click(screen.getByRole("button", { name: "Ignored values" }));
  paint();
  expect(screen.getByLabelText("Search ignored values")).toBe(search);
  expect(search.value).toBe("fixture");
  expect(screen.getAllByTestId("resident-surface").filter((node) => node.dataset.resident === "active")).toHaveLength(1);
});
