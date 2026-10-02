import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { ManagedSecret } from "../../api/types.ts";
import { ManagedSecretsPanel, SECRETS_PAGE_SIZE } from "./ManagedSecretsPanel.tsx";

const revealManagedSecret = vi.hoisted(() => vi.fn());
vi.mock("../../platform/presence.ts", () => ({
  revealManagedSecret,
  PresenceError: class PresenceError extends Error {},
}));

const clipboard = vi.hoisted(() => ({
  read: vi.fn(async () => ({ readable: true, text: "" }) as
    | { readable: true; text: string }
    | { readable: false }),
  write: vi.fn(async () => {}),
  copy: vi.fn(async () => {}),
}));
vi.mock("../../utils/clipboard.ts", () => ({
  readClipboardText: clipboard.read,
  writeClipboardText: clipboard.write,
  copyTextToClipboard: clipboard.copy,
}));

const REVEALED = "native-authenticated-value";

beforeEach(() => {
  vi.spyOn(document, "hasFocus").mockReturnValue(true);
});

function revealResolves(seconds = 30) {
  revealManagedSecret.mockResolvedValue({
    secret_value: REVEALED,
    version: 1,
    revealed_at: "2026-09-01T12:00:00Z",
    remask_after_ms: seconds * 1000,
  });
}

const SECRET_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

const secret: ManagedSecret = {
  reference: `{{paintedwolf-secret:${SECRET_ID}}}`,
  name: "Webhook signing key",
  purpose: "Authenticates callbacks",
  scope: "project",
  origin: "generated",
  format: "base64url",
  entropy_bits: 256,
  created_at: "2026-08-31T10:00:00Z",
  state: "active",
  version: 1,
  use_count: 0,
  reveal_count: 0,
  release_count: 0,
};

function chatSecret(index: number): ManagedSecret {
  const id = `bbbbbbbb-bbbb-4bbb-8bbb-${String(index).padStart(12, "0")}`;
  return {
    reference: `{{paintedwolf-secret:${id}}}`,
    name: `Chat token ${index}`,
    scope: "chat",
    origin: "generated",
    chat_session_id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    chat_title: "Provision staging",
    format: "hex",
    entropy_bits: 128,
    created_at: `2026-08-${String(10 + index).padStart(2, "0")}T10:00:00Z`,
    state: "active",
    version: 1,
    use_count: 0,
    reveal_count: 0,
    release_count: 0,
  };
}

function clientWith(items: ManagedSecret[], extra: Partial<LycaonClient> = {}) {
  return stubClient({
    getProject: vi.fn(async () => ({ roots: [] })),
    listProjectManagedSecrets: vi.fn(async () => ({
      secrets: items,
      count: items.length,
    })),
    listProjectManagedSecretUses: vi.fn(async () => ({ uses: [], count: 0 })),
    listProjectManagedSecretAttestations: vi.fn(async () => ({ attestations: [] })),
    ...extra,
  });
}

function renderPanel(client: LycaonClient) {
  render(() => <ManagedSecretsPanel client={client} projectId="project-1" />);
}

async function openDetail(id: string) {
  fireEvent.click(await screen.findByTestId(`managed-secret-${id}`));
  return screen.findByTestId("managed-secret-card");
}

afterEach(() => {
  vi.useRealTimers();
  revealManagedSecret.mockReset();
  clipboard.read.mockReset();
  clipboard.read.mockResolvedValue({ readable: true, text: "" });
  clipboard.write.mockReset();
  clipboard.write.mockResolvedValue(undefined);
  clipboard.copy.mockReset();
  vi.restoreAllMocks();
});

/** Opens the detail pane and reveals, returning once the value is on screen. */
async function revealValue() {
  renderPanel(clientWith([secret]));
  await openDetail(SECRET_ID);
  fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
  await screen.findByText(REVEALED);
}

describe("ManagedSecretsPanel", () => {
  it.each(["blur", "hidden"])("discards a reveal completed after %s", async (event) => {
    let complete!: (value: unknown) => void;
    revealManagedSecret.mockReturnValue(new Promise((resolve) => { complete = resolve; }));
    renderPanel(clientWith([secret]));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    if (event === "blur") {
      vi.mocked(document.hasFocus).mockReturnValue(false);
      window.dispatchEvent(new Event("blur"));
    } else {
      vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
      document.dispatchEvent(new Event("visibilitychange"));
    }
    complete({ secret_value: REVEALED, version: 1, revealed_at: "2026-09-01T12:00:00Z", remask_after_ms: 30000 });
    await waitFor(() => expect((screen.getByTestId("managed-secret-reveal-button") as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByText(REVEALED)).toBeNull();
  });

  it("accepts completion after focus returns from native authentication", async () => {
    let complete!: (value: unknown) => void;
    revealManagedSecret.mockReturnValue(new Promise((resolve) => { complete = resolve; }));
    renderPanel(clientWith([secret]));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    vi.mocked(document.hasFocus).mockReturnValue(false);
    window.dispatchEvent(new Event("blur"));
    vi.mocked(document.hasFocus).mockReturnValue(true);
    window.dispatchEvent(new Event("focus"));
    complete({ secret_value: REVEALED, version: 1, revealed_at: "2026-09-01T12:00:00Z", remask_after_ms: 30000 });
    expect(await screen.findByText(REVEALED)).toBeTruthy();
  });

  it("does not revive a request after navigating away and back to the same secret", async () => {
    let complete!: (value: unknown) => void;
    revealManagedSecret.mockReturnValueOnce(new Promise((resolve) => { complete = resolve; }));
    renderPanel(clientWith([secret]));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    fireEvent.click(screen.getByTestId("managed-secrets-back"));
    await openDetail(SECRET_ID);
    complete({ secret_value: "stale-value", version: 1, revealed_at: "2026-09-01T12:00:00Z", remask_after_ms: 30000 });
    revealResolves();
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    expect(await screen.findByText(REVEALED)).toBeTruthy();
    expect(screen.queryByText("stale-value")).toBeNull();
  });

  it("keeps a newer reveal pending when an obsolete request completes", async () => {
    let finishOld!: (value: unknown) => void;
    let finishNew!: (value: unknown) => void;
    revealManagedSecret
      .mockReturnValueOnce(new Promise((resolve) => { finishOld = resolve; }))
      .mockReturnValueOnce(new Promise((resolve) => { finishNew = resolve; }));
    renderPanel(clientWith([secret]));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    fireEvent.click(screen.getByTestId("managed-secrets-back"));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    const result = { secret_value: REVEALED, version: 1, revealed_at: "2026-09-01T12:00:00Z", remask_after_ms: 30000 };
    finishOld(result);
    await Promise.resolve();
    expect((screen.getByTestId("managed-secret-reveal-button") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByText(REVEALED)).toBeNull();
    finishNew(result);
    expect(await screen.findByText(REVEALED)).toBeTruthy();
  });

  it("does not revive a request after hiding and returning to the window", async () => {
    let complete!: (value: unknown) => void;
    revealManagedSecret.mockReturnValue(new Promise((resolve) => { complete = resolve; }));
    renderPanel(clientWith([secret]));
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    visibility.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    complete({ secret_value: REVEALED, version: 1, revealed_at: "2026-09-01T12:00:00Z", remask_after_ms: 30000 });
    await Promise.resolve();
    expect(screen.queryByText(REVEALED)).toBeNull();
  });
  it.each([
    ["project", "Project Every chat in this project can use it."],
    ["chat", "Chat Only the chat that created it, and that chat's workers, can use it."],
  ] as const)("separates %s fact values from explanatory text", async (scope, expectedScope) => {
    renderPanel(clientWith([{ ...secret, scope }]));
    await openDetail(SECRET_ID);

    expect(screen.getByText("Scope").nextElementSibling?.textContent).toBe(expectedScope);
    expect(screen.getByText("Last revealed").nextElementSibling?.textContent)
      .toBe("Never Never revealed");
  });

  it("lists secrets in scope groups without requesting a value", async () => {
    const client = clientWith([secret, chatSecret(1)]);

    renderPanel(client);

    expect(await screen.findByTestId(`managed-secret-${SECRET_ID}`)).toBeTruthy();
    expect(screen.getByTestId("managed-secrets-group-project")).toBeTruthy();
    expect(screen.getByTestId("managed-secrets-group-chat")).toBeTruthy();
    expect(screen.getByTestId("managed-secrets-count").textContent).toContain("2 secrets");
    expect(screen.getByText("Authenticates callbacks")).toBeTruthy();
    expect(screen.queryByText(secret.reference)).toBeNull();
    expect(client.listProjectManagedSecrets).toHaveBeenCalledWith("project-1");
  });

  it("opens a detail pane carrying the reference and host metadata", async () => {
    const client = clientWith([secret]);

    renderPanel(client);
    await openDetail(SECRET_ID);

    expect(screen.getByText(secret.reference)).toBeTruthy();
    expect(screen.getByText("base64url")).toBeTruthy();
    expect(screen.getByText("256 bits")).toBeTruthy();
    expect(screen.getByText("No agent-use deadline")).toBeTruthy();
    expect(screen.getByText("Version 1")).toBeTruthy();
    expect(screen.getByText("Original stored value")).toBeTruthy();
    expect(screen.getByText("Every chat in this project can use it.")).toBeTruthy();

    fireEvent.click(screen.getByTestId("managed-secrets-back"));
    await waitFor(() => expect(screen.queryByTestId("managed-secret-card")).toBeNull());
  });

  it("reveals only through native authentication and remasks on blur", async () => {
    revealManagedSecret.mockResolvedValue({
      secret_value: "native-authenticated-value",
      version: 1,
      revealed_at: "2026-09-01T12:00:00Z",
      remask_after_ms: 30000,
    });
    const client = clientWith([secret]);

    renderPanel(client);
    await openDetail(SECRET_ID);
    expect(screen.queryByText("native-authenticated-value")).toBeNull();
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));

    expect(await screen.findByText("native-authenticated-value")).toBeTruthy();
    expect(revealManagedSecret).toHaveBeenCalledWith("project-1", SECRET_ID);
    expect(screen.getByText("1 reveal")).toBeTruthy();
    window.dispatchEvent(new Event("blur"));
    await waitFor(() =>
      expect(screen.queryByText("native-authenticated-value")).toBeNull(),
    );
  });

  it("remasks when the reveal deadline passes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    revealResolves(30);

    await revealValue();
    expect(screen.getByText(/Hides in 30 seconds/)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(29_000);
    expect(screen.queryByText(REVEALED)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1_500);

    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
  });

  it("remasks when the window stops being visible", async () => {
    revealResolves();
    await revealValue();

    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));

    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
  });

  it("stays revealed while the window is still visible", async () => {
    revealResolves();
    await revealValue();

    document.dispatchEvent(new Event("visibilitychange"));

    expect(screen.getByText(REVEALED)).toBeTruthy();
  });

  it("restarts the countdown when the value is revealed a second time", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    revealResolves(30);

    await revealValue();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(screen.getByText(/Hides in 10 seconds/)).toBeTruthy();

    fireEvent.click(screen.getByTestId("managed-secret-hide-value"));
    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    await screen.findByText(REVEALED);

    expect(screen.getByText(/Hides in 30 seconds/)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(screen.getByText(REVEALED)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(16_000);
    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
  });

  /** Copy succeeds only after the clipboard write completes. */
  it("claims the copy only once the clipboard write lands", async () => {
    revealResolves();
    let settle: () => void = () => {};
    clipboard.write.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          settle = resolve;
        }),
    );
    await revealValue();

    const copy = screen.getByTestId("managed-secret-copy-value");
    fireEvent.click(copy);
    expect(clipboard.write).toHaveBeenCalledWith(REVEALED);
    expect(copy.textContent).toBe("Copy value");

    settle();
    await waitFor(() => expect(copy.textContent).toBe("Value copied"));
  });

  it("does not attribute an old clipboard completion to a newer reveal", async () => {
    revealResolves();
    let finishCopy!: () => void;
    clipboard.write.mockReturnValueOnce(new Promise<void>((resolve) => { finishCopy = resolve; }));
    await revealValue();
    fireEvent.click(screen.getByTestId("managed-secret-copy-value"));
    fireEvent.click(screen.getByTestId("managed-secret-hide-value"));
    fireEvent.click(screen.getByTestId("managed-secret-reveal-button"));
    await screen.findByText(REVEALED);
    finishCopy();
    await Promise.resolve();
    expect(screen.getByTestId("managed-secret-copy-value").textContent).toBe("Copy value");
  });

  it("reports a refused clipboard write instead of claiming a copy", async () => {
    revealResolves();
    clipboard.write.mockRejectedValue(new Error("document is not focused"));
    await revealValue();

    fireEvent.click(screen.getByTestId("managed-secret-copy-value"));

    await waitFor(() =>
      expect(screen.getByTestId("managed-secret-copy-value").textContent).toBe(
        "Could not copy",
      ),
    );
  });

  it("leaves what the user copied on the clipboard when the value hides", async () => {
    revealResolves();
    await revealValue();

    fireEvent.click(screen.getByTestId("managed-secret-copy-value"));
    await waitFor(() => expect(clipboard.write).toHaveBeenCalledWith(REVEALED));

    fireEvent.click(screen.getByTestId("managed-secret-hide-value"));
    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
    window.dispatchEvent(new Event("blur"));

    expect(clipboard.write).toHaveBeenCalledTimes(1);
    expect(clipboard.read).not.toHaveBeenCalled();
  });

  /** Rejected saves retain the mounted form and its draft. */
  it("keeps a typed replacement value when the save is rejected", async () => {
    const pasted = "sk_live_".concat("a".repeat(56));
    const replaceProjectManagedSecretValue = vi.fn(async () => {
      throw new Error("The value could not be replaced.");
    });
    renderPanel(clientWith([secret], { replaceProjectManagedSecretValue }));
    await openDetail(SECRET_ID);

    fireEvent.click(screen.getByTestId("managed-secret-replace-value"));
    const field = screen.getByTestId("managed-secret-new-value") as HTMLInputElement;
    fireEvent.input(field, { target: { value: pasted } });
    expect(field.value).toBe(pasted);

    fireEvent.click(screen.getByTestId("managed-secret-value-submit"));

    await waitFor(() => expect(replaceProjectManagedSecretValue).toHaveBeenCalled());
    await screen.findByTestId("managed-secrets-error");
    expect(screen.getByTestId("managed-secret-value-form")).toBeTruthy();
    expect(
      (screen.getByTestId("managed-secret-new-value") as HTMLInputElement).value,
    ).toBe(pasted);
  });

  it("never touches the clipboard when nothing was copied", async () => {
    revealResolves();
    await revealValue();

    fireEvent.click(screen.getByTestId("managed-secret-hide-value"));

    await waitFor(() => expect(screen.queryByText(REVEALED)).toBeNull());
    expect(clipboard.read).not.toHaveBeenCalled();
    expect(clipboard.write).not.toHaveBeenCalled();
  });

  it("copies only the highlighted characters, not the whole value", async () => {
    revealResolves();
    await revealValue();

    vi.spyOn(document, "getSelection").mockReturnValue({
      toString: () => "nat",
    } as unknown as Selection);
    fireEvent.copy(screen.getByTestId("managed-secret-revealed-value"));

    // The copy event already contains the selection.
    expect(clipboard.write).not.toHaveBeenCalled();
    expect(screen.getByTestId("managed-secret-copy-value").textContent).toBe(
      "Value copied",
    );
  });

  it("stores an entered value and never asks for it back", async () => {
    const created: ManagedSecret = {
      ...secret,
      reference: "{{paintedwolf-secret:dddddddd-dddd-4ddd-8ddd-dddddddddddd}}",
      name: "Stripe test key",
      purpose: "Charges the test-mode checkout",
      origin: "settings_entered",
      format: undefined,
      entropy_bits: undefined,
    };
    const client = clientWith([], {
      createProjectManagedSecret: vi.fn(async () => created),
    });

    renderPanel(client);
    fireEvent.click(await screen.findByTestId("managed-secrets-add"));
    fireEvent.input(screen.getByTestId("managed-secret-add-name"), {
      target: { value: "Stripe test key" },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-purpose"), {
      target: { value: "Charges the test-mode checkout" },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-value"), {
      target: { value: "sk-test-entered-value" },
    });
    fireEvent.click(screen.getByTestId("managed-secret-add-submit"));

    await waitFor(() =>
      expect(client.createProjectManagedSecret).toHaveBeenCalledWith("project-1", {
        operation_id: expect.any(String),
        name: "Stripe test key",
        purpose: "Charges the test-mode checkout",
        secret_value: "sk-test-entered-value",
      }),
    );
    const card = await screen.findByTestId("managed-secret-card");
    expect(card.textContent).not.toContain("sk-test-entered-value");
    expect(screen.getByText("Entered in settings")).toBeTruthy();
  });

  it("dismisses the agent-use deadline choices when the add form is clicked elsewhere", async () => {
    renderPanel(clientWith([]));
    fireEvent.click(await screen.findByTestId("managed-secrets-add"));

    const deadline = screen.getByTestId("managed-secret-agent-use-deadline-choice");
    fireEvent.click(deadline);
    expect(screen.getByRole("listbox", { name: "Agent-use deadline" })).toBeTruthy();

    fireEvent.mouseDown(screen.getByTestId("managed-secret-add-name"));
    expect(screen.queryByRole("listbox", { name: "Agent-use deadline" })).toBeNull();
  });

  it("wires the value reveal and a custom agent-use deadline into creation", async () => {
    const created: ManagedSecret = {
      ...secret,
      origin: "settings_entered",
    };
    const client = clientWith([], {
      createProjectManagedSecret: vi.fn(async () => created),
    });
    const chosen = new Date();
    chosen.setDate(chosen.getDate() + 7);
    const customDate = [
      chosen.getFullYear(),
      String(chosen.getMonth() + 1).padStart(2, "0"),
      String(chosen.getDate()).padStart(2, "0"),
    ].join("-");

    renderPanel(client);
    fireEvent.click(await screen.findByTestId("managed-secrets-add"));
    fireEvent.input(screen.getByTestId("managed-secret-add-name"), {
      target: { value: "  Registry token  " },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-purpose"), {
      target: { value: "  Publishes packages  " },
    });
    const valueInput = screen.getByTestId<HTMLInputElement>(
      "managed-secret-add-value",
    );
    expect(valueInput.type).toBe("password");
    fireEvent.input(valueInput, { target: { value: "  secret-bytes  " } });
    fireEvent.click(screen.getByTestId("managed-secret-add-value-reveal"));
    expect(valueInput.type).toBe("text");

    fireEvent.click(screen.getByTestId("managed-secret-agent-use-deadline-choice"));
    fireEvent.click(screen.getByRole("option", { name: "On a date" }));
    fireEvent.input(screen.getByTestId("managed-secret-agent-use-deadline-date"), {
      target: { value: customDate },
    });
    fireEvent.click(screen.getByTestId("managed-secret-add-submit"));

    await waitFor(() =>
      expect(client.createProjectManagedSecret).toHaveBeenCalledWith("project-1", {
        operation_id: expect.any(String),
        name: "Registry token",
        purpose: "Publishes packages",
        secret_value: "  secret-bytes  ",
        agent_use_ends_at: new Date(`${customDate}T23:59:59`).toISOString(),
      }),
    );
  });

  it("refuses to submit an add with no purpose and does not call the host", async () => {
    const client = clientWith([], {
      createProjectManagedSecret: vi.fn(async () => secret),
    });

    renderPanel(client);
    fireEvent.click(await screen.findByTestId("managed-secrets-add"));
    fireEvent.input(screen.getByTestId("managed-secret-add-name"), {
      target: { value: "Unlabelled" },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-value"), {
      target: { value: "sk-test-entered-value" },
    });
    fireEvent.click(screen.getByTestId("managed-secret-add-submit"));

    expect(await screen.findByText("Say what this secret is for.")).toBeTruthy();
    expect(client.createProjectManagedSecret).not.toHaveBeenCalled();
  });

  it("says when a value is already managed instead of adding a second reference", async () => {
    const client = clientWith([secret], {
      createProjectManagedSecret: vi.fn(async () => secret),
    });

    renderPanel(client);
    fireEvent.click(await screen.findByTestId("managed-secrets-add"));
    fireEvent.input(screen.getByTestId("managed-secret-add-name"), {
      target: { value: "Another name" },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-purpose"), {
      target: { value: "Same bytes" },
    });
    fireEvent.input(screen.getByTestId("managed-secret-add-value"), {
      target: { value: "sk-test-entered-value" },
    });
    fireEvent.click(screen.getByTestId("managed-secret-add-submit"));

    const note = await screen.findByTestId("managed-secrets-note");
    expect(note.textContent).toContain("Webhook signing key");
  });

  it("replaces the stored value while keeping the same reference", async () => {
    const replaced: ManagedSecret = {
      ...secret,
      version: 2,
      value_replaced_at: "2026-09-01T09:00:00Z",
    };
    const client = clientWith([secret], {
      replaceProjectManagedSecretValue: vi.fn(async () => replaced),
    });

    renderPanel(client);
    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-replace-value"));
    fireEvent.input(screen.getByTestId("managed-secret-new-value"), {
      target: { value: "sk-test-replacement-value" },
    });
    fireEvent.click(screen.getByTestId("managed-secret-value-submit"));

    await waitFor(() =>
      expect(client.replaceProjectManagedSecretValue).toHaveBeenCalledWith(
        "project-1",
        SECRET_ID,
        { secret_value: "sk-test-replacement-value" },
      ),
    );
    expect(await screen.findByText("Version 2")).toBeTruthy();
    expect(screen.getByText(secret.reference)).toBeTruthy();
  });

  it("warns that replacing a file-marked value does not update the file", async () => {
    const marked: ManagedSecret = { ...secret, origin: "file_marked" };
    renderPanel(clientWith([marked]));

    await openDetail(SECRET_ID);
    fireEvent.click(screen.getByTestId("managed-secret-replace-value"));

    expect(screen.getByTestId("managed-secret-value-form").textContent).toContain(
      "does not update that file",
    );
  });

  it("leads an unavailable secret with restoring its value", async () => {
    const lost: ManagedSecret = { ...secret, state: "unavailable" };
    const client = clientWith([lost]);

    renderPanel(client);
    await openDetail(SECRET_ID);

    expect(screen.getByTestId("managed-secret-replace-value").textContent).toContain(
      "Restore value",
    );
    fireEvent.click(screen.getByTestId("managed-secret-replace-value"));
    expect(
      screen.getByTestId("managed-secret-value-form").textContent,
    ).toContain("no readable value");
  });

  it("promotes a chat secret so every chat in the project can use it", async () => {
    const chat = { ...chatSecret(1), purpose: "Publishes releases" };
    const promoted: ManagedSecret = {
      ...chat,
      scope: "project",
      chat_session_id: undefined,
      chat_title: undefined,
    };
    const id = chat.reference.slice("{{paintedwolf-secret:".length, -2);
    const client = clientWith([chat], {
      updateProjectManagedSecret: vi.fn(async () => promoted),
    });

    renderPanel(client);
    await openDetail(id);
    fireEvent.click(screen.getByTestId("managed-secret-promote"));

    await waitFor(() =>
      expect(client.updateProjectManagedSecret).toHaveBeenCalledWith(
        "project-1",
        id,
        { scope: "project" },
      ),
    );
    await waitFor(() =>
      expect(screen.queryByTestId("managed-secret-promote")).toBeNull(),
    );
  });

  it("names the chat a chat secret belongs to", async () => {
    const live = chatSecret(1);
    renderPanel(clientWith([live]));
    await openDetail(live.reference.slice("{{paintedwolf-secret:".length, -2));

    expect(screen.getByText("Provision staging")).toBeTruthy();
    expect(screen.queryByTestId("managed-secret-chat-deleted")).toBeNull();
  });

  it("keeps a deleted chat's secret manageable and says no agent can use it", async () => {
    const orphan: ManagedSecret = { ...chatSecret(1), chat_title: undefined, chat_deleted: true };
    renderPanel(clientWith([orphan]));
    await openDetail(orphan.reference.slice("{{paintedwolf-secret:".length, -2));

    const hint = screen.getByTestId("managed-secret-chat-deleted");
    expect(hint.parentElement?.textContent).toContain("Deleted chat");
    expect(screen.getByTestId("managed-secret-promote")).toBeTruthy();
  });

  it("offers a revoked secret nothing but its reference", async () => {
    const revoked: ManagedSecret = { ...secret, state: "revoked" };
    const client = clientWith([revoked], {
      listProjectManagedSecrets: vi.fn(async () => ({
        secrets: [revoked],
        count: 1,
      })),
    });

    renderPanel(client);
    expect(await screen.findByTestId("managed-secrets-show-revoked")).toBeTruthy();
    fireEvent.click(screen.getByTestId("managed-secrets-show-revoked"));
    await openDetail(SECRET_ID);

    expect(screen.getByTestId("managed-secret-terminal")).toBeTruthy();
    expect(screen.queryByTestId("managed-secret-replace-value")).toBeNull();
    expect(screen.queryByTestId("managed-secret-edit")).toBeNull();
    expect(screen.queryByTestId(`managed-secret-revoke-${SECRET_ID}`)).toBeNull();
    expect(screen.getByTestId("managed-secret-copy")).toBeTruthy();
  });

  it("confirms revocation and replaces the row with host metadata", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let currentSecret = secret;
    const listProjectManagedSecrets = vi.fn(async () => ({ secrets: [currentSecret] }));
    const client = clientWith([secret], {
      revokeProjectManagedSecret: vi.fn(async () => {
        currentSecret = { ...secret, name: "Revoked webhook key", state: "revoked" };
      }),
      listProjectManagedSecrets,
    });

    renderPanel(client);
    await openDetail(SECRET_ID);
    fireEvent.click(await screen.findByTestId(`managed-secret-revoke-${SECRET_ID}`));

    await waitFor(() =>
      expect(client.revokeProjectManagedSecret).toHaveBeenCalledWith(
        "project-1",
        SECRET_ID,
      ),
    );
    await waitFor(() =>
      expect(screen.queryByTestId(`managed-secret-revoke-${SECRET_ID}`)).toBeNull(),
    );
    expect(screen.getByTestId("managed-secret-terminal")).toBeTruthy();
    expect(listProjectManagedSecrets).toHaveBeenLastCalledWith("project-1");
    expect(screen.getAllByText("Revoked webhook key").length).toBeGreaterThan(0);
  });

  it("shows what the host did with a reference, refusals included", async () => {
    const client = clientWith([secret], {
      listProjectManagedSecretUses: vi.fn(async () => ({
        uses: [
          {
            used_at: "2026-09-01T09:00:00Z",
            tool_name: "http_request",
            outcome: "resolved" as const,
            delivery: "handed_off" as const,
            tool_call_id: "call-history",
            version: 1,
          },
          {
            used_at: "2026-09-01T08:00:00Z",
            tool_name: "command",
            outcome: "agent_use_expired" as const,
            delivery: "not_dispatched" as const,
          },
        ],
        count: 2,
      })),
    });

    renderPanel(client);
    await openDetail(SECRET_ID);

    expect(await screen.findByText("http_request")).toBeTruthy();
    expect(screen.getByText("Handed to executor or transport")).toBeTruthy();
    expect(screen.getByText("call-history")).toBeTruthy();
    expect(screen.getByText("Refused — agent use expired")).toBeTruthy();
    expect(client.listProjectManagedSecretUses).toHaveBeenCalledWith(
      "project-1",
      SECRET_ID,
    );
  });

  it("filters a long inventory down by name", async () => {
    const items = [secret, ...Array.from({ length: 4 }, (_, i) => chatSecret(i + 1))];
    const client = clientWith(items);

    renderPanel(client);
    await screen.findByTestId(`managed-secret-${SECRET_ID}`);
    fireEvent.input(screen.getByTestId("managed-secrets-filter-query"), {
      target: { value: "webhook" },
    });

    await waitFor(() =>
      expect(screen.getByTestId("managed-secrets-count").textContent).toContain(
        "1 secret",
      ),
    );
    expect(screen.queryByText("Chat token 1")).toBeNull();
  });

  it("surfaces a load failure as an inline notice", async () => {
    const client = stubClient({
      listProjectManagedSecrets: vi.fn(async () => {
        throw new Error("offline");
      }),
    });

    renderPanel(client);

    expect(await screen.findByTestId("managed-secrets-error")).toBeTruthy();
  });

  it("shows the empty state only once loading resolves", async () => {
    const client = clientWith([]);

    renderPanel(client);

    expect(await screen.findByTestId("managed-secrets-empty")).toBeTruthy();
    expect(
      screen
        .getByTestId("managed-secrets-list-panel")
        .classList.contains("den-managed-secrets-panel"),
    ).toBe(true);
    expect(
      screen
        .getByTestId("managed-secrets-list-panel")
        .querySelector(".den-settings-list-inbox__list")
        ?.getAttribute("data-den-scrollport"),
    ).toBe("y");
    expect(screen.getByTestId("managed-secrets-count").textContent).toContain(
      "0 secrets",
    );
  });

  it("keeps Add secret inside the stable inventory surface", async () => {
    const client = clientWith([]);

    renderPanel(client);

    const panel = await screen.findByTestId("managed-secrets-list-panel");
    fireEvent.click(screen.getByTestId("managed-secrets-add"));

    expect(await screen.findByTestId("managed-secret-add-form")).toBeTruthy();
    expect(screen.getByTestId("managed-secrets-list-panel")).toBe(panel);
    expect(panel.classList.contains("den-managed-secrets-panel")).toBe(true);
  });

  it("pages a long inventory", async () => {
    const items = Array.from({ length: SECRETS_PAGE_SIZE + 3 }, (_, i) =>
      chatSecret(i + 1),
    );
    const client = clientWith(items);

    renderPanel(client);

    const pager = await screen.findByTestId("managed-secrets-pager");
    expect(pager.textContent).toContain(`1–${SECRETS_PAGE_SIZE} of ${items.length}`);
    fireEvent.click(screen.getByTestId("managed-secrets-pager-next"));
    await waitFor(() =>
      expect(screen.getByTestId("managed-secrets-pager").textContent).toContain(
        `${SECRETS_PAGE_SIZE + 1}–${items.length} of ${items.length}`,
      ),
    );
  });
});
