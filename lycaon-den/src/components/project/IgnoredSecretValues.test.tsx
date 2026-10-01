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
    fireEvent.click(await screen.findByRole("button", { name: "Review ignored value: Published example" }));
    expect((screen.getByLabelText("Exact public value") as HTMLTextAreaElement).value).toBe("public fixture");
    expect(screen.getByText(/Protected credential evidence/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open ignore file" })).toBeTruthy();
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
    const add = await screen.findByRole("button", { name: "Add value" }) as HTMLButtonElement;
    // Declarations need a folder, so adding opens once the project's folders load.
    await waitFor(() => expect(add.disabled).toBe(false));
    fireEvent.click(add);
    fireEvent.input(screen.getByLabelText("Exact public value"), { target: { value: "an-example" } });
    fireEvent.input(screen.getByLabelText("Reason"), { target: { value: "  Vendor sample  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save declaration" }));

    await waitFor(() => expect(createProjectSecretIgnore).toHaveBeenCalledWith("project", {
      root_id: "root",
      entry: { id: expect.any(String), value: "an-example", reason: "Vendor sample", expires_on: undefined },
    }));
    expect(createProjectSecretIgnore).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("1 ignored value")).toBeTruthy();
  });

  it("withdraws a declaration once the removal is confirmed", async () => {
    const deleteProjectSecretIgnore = vi.fn(async () => {});
    const client = stubClient({
      getProject: vi.fn(async () => project),
      listProjectSecretIgnores: vi.fn(async () => ({ rules: [rule] })),
      deleteProjectSecretIgnore,
    });
    render(() => <IgnoredSecretValues client={client} projectId="project" />);
    fireEvent.click(await screen.findByRole("button", { name: "Review ignored value: Published example" }));

    confirmDestructive.mockResolvedValueOnce(false);
    fireEvent.click(screen.getByRole("button", { name: "Remove declaration" }));
    await waitFor(() => expect(confirmDestructive).toHaveBeenCalledTimes(1));
    expect(deleteProjectSecretIgnore).not.toHaveBeenCalled();

    confirmDestructive.mockResolvedValueOnce(true);
    fireEvent.click(screen.getByRole("button", { name: "Remove declaration" }));
    await waitFor(() => expect(deleteProjectSecretIgnore).toHaveBeenCalledWith("project", "fixture", "root"));
    expect(await screen.findByText("No ignored public values.")).toBeTruthy();
  });
});
