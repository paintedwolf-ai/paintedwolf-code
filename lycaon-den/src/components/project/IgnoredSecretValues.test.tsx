import { assertNamedControl } from "../../test/controls.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { IgnoredSecretValues } from "./IgnoredSecretValues.tsx";

const confirmDestructive = vi.fn();
vi.mock("../../platform/interaction/confirm-dialog.ts", () => ({
  confirmDestructive: (...args: unknown[]) => confirmDestructive(...args),
}));

const rule = {
  id: "fixture",
  root_id: "root",
  path: "/project/.paintedwolf/ignores.yaml",
  value: "public fixture",
  reason: "Published example",
  status: "protected",
};

const project = { roots: [{ id: "root", label: "App", path: "/project" }] };

describe("ignored public values", () => {
  it("shows file declarations and protected precedence", async () => {
    const client = stubClient({
      getProject: vi.fn(async () => project),
      listProjectSecretIgnores: vi.fn(async () => ({ rules: [rule] })),
    });
    render(() => <IgnoredSecretValues client={client} projectId="project" />);
    fireEvent.click(assertNamedControl(await screen.findByTestId("ignored-value-fixture"), "button"));
    expect((screen.getByTestId("ignored-value-input") as HTMLTextAreaElement).value).toBe("public fixture");
  });

  it("declares a value in the chosen folder and lists what the file now holds", async () => {
    const createProjectSecretIgnore = vi.fn(async () => ({
      rules: [{ ...rule, id: "added", value: "an-example", reason: "Vendor sample", status: "active" as const }],
    }));
    const client = stubClient({
      getProject: vi.fn(async () => project),
      listProjectSecretIgnores: vi.fn(async () => ({ rules: [] })),
      createProjectSecretIgnore,
    });
    render(() => <IgnoredSecretValues client={client} projectId="project" />);
    const add = assertNamedControl(await screen.findByTestId("add-value"), "button") as HTMLButtonElement;
    // Declarations need a folder, so adding opens once the project's folders load.
    await waitFor(() => expect(add.disabled).toBe(false));
    fireEvent.click(add);
    fireEvent.input(screen.getByTestId("add-value-input"), { target: { value: "an-example" } });
    fireEvent.input(screen.getByTestId("add-reason-input"), { target: { value: "  Vendor sample  " } });
    fireEvent.click(assertNamedControl(screen.getByTestId("save-declaration"), "button"));

    await waitFor(() => expect(createProjectSecretIgnore).toHaveBeenCalledWith("project", {
      root_id: "root",
      entry: { id: expect.any(String), value: "an-example", reason: "Vendor sample", expires_on: undefined },
    }));
    expect(createProjectSecretIgnore).toHaveBeenCalledTimes(1);
    expect(await screen.findByTestId("ignored-value-added")).toBeTruthy();
  });

  it("withdraws a declaration once the removal is confirmed", async () => {
    const deleteProjectSecretIgnore = vi.fn(async () => {});
    const client = stubClient({
      getProject: vi.fn(async () => project),
      listProjectSecretIgnores: vi.fn(async () => ({ rules: [rule] })),
      deleteProjectSecretIgnore,
    });
    render(() => <IgnoredSecretValues client={client} projectId="project" />);
    fireEvent.click(assertNamedControl(await screen.findByTestId("ignored-value-fixture"), "button"));

    confirmDestructive.mockResolvedValueOnce(false);
    fireEvent.click(assertNamedControl(screen.getByTestId("remove-declaration"), "button"));
    await waitFor(() => expect(confirmDestructive).toHaveBeenCalledTimes(1));
    expect(deleteProjectSecretIgnore).not.toHaveBeenCalled();

    confirmDestructive.mockResolvedValueOnce(true);
    fireEvent.click(assertNamedControl(screen.getByTestId("remove-declaration"), "button"));
    await waitFor(() => expect(deleteProjectSecretIgnore).toHaveBeenCalledWith("project", "fixture", "root"));
    expect(await screen.findByTestId("ignored-values-empty")).toBeTruthy();
  });
});
