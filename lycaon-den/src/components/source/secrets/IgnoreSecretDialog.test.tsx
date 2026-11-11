import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { assertNamedControl } from "../../../test/controls.ts";
import { IgnoreSecretDialog } from "./IgnoreSecretDialog.tsx";

const project = { roots: [{ id: "root", label: "App", path: "/project" }] };
describe("project secret ignore review", () => {
  it("reviews exact bytes and requires a reason before writing the selected project file", async () => {
    const add = vi.fn(async () => ({ rules: [] }));
    const close = vi.fn();
    const client = stubClient({ getProject: vi.fn(async () => project), createProjectSecretIgnore: add });
    render(() => <IgnoreSecretDialog client={client} target={{ projectId: "project", value: async () => "  public fixture\n" }} onClose={close} />);
    const value = await screen.findByTestId("ignore-secret-value") as HTMLTextAreaElement;
    expect(value.value).toBe("  public fixture\n");
    expect((assertNamedControl(screen.getByTestId("ignore-secret-save"), "button") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.input(screen.getByTestId("ignore-secret-reason"), { target: { value: "Published example" } });
    fireEvent.click(assertNamedControl(screen.getByTestId("ignore-secret-save"), "button"));
    await waitFor(() => expect(add).toHaveBeenCalledOnce());
    expect(add.mock.calls[0]).toEqual(["project", { root_id: "root", entry: { id: expect.any(String), value: "  public fixture\n", reason: "Published example", expires_on: undefined } }]);
    await waitFor(() => expect(close).toHaveBeenCalledOnce());
  });
  it("does not offer a write when the live review expired", async () => {
    const add = vi.fn();
    const client = stubClient({ getProject: vi.fn(async () => project), createProjectSecretIgnore: add });
    render(() => <IgnoreSecretDialog client={client} target={{ projectId: "project", value: async () => { throw new Error("expired"); } }} onClose={() => {}} />);
    await screen.findByRole("alert");
    expect(screen.queryByTestId("ignore-secret-save")).toBeNull();
    expect(add).not.toHaveBeenCalled();
  });
});
