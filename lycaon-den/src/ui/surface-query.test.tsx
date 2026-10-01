import { render, screen, waitFor } from "@solidjs/testing-library";
import { createMemo, createSignal, For } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createPreparation } from "./presentation.ts";
import { PresentationProvider } from "./presentation-context.tsx";
import { ResidentPresenceProvider } from "./resident-presence-context.tsx";
import { createSurfaceQuery } from "./surface-query.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

describe("surface queries", () => {
  it("publishes a command reply to the identity captured before navigation", async () => {
    const client = {};
    const [key, setKey] = createSignal("a");
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string }, string>>;
    render(() => {
      query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: key() }), load: async ({ key }) => key });
      return null;
    });
    await waitFor(() => expect(query.value()).toBe("a"));
    const command = query.capture();
    setKey("b");
    await waitFor(() => expect(query.value()).toBe("b"));
    command.publish("updated a");
    expect(query.value()).toBe("b");
    expect(command.current()).toBe(false);
  });
  it("shares acquisition and prepares only after the complete projection settles", async () => {
    const client = {};
    const pending = deferred<string[]>();
    const load = vi.fn(() => pending.promise);
    const preparation = createPreparation();
    const Consumer = () => {
      const query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return <span>{query.value()?.join(",")}</span>;
    };
    const view = render(() => <PresentationProvider preparation={preparation}><Consumer /><Consumer /></PresentationProvider>);
    expect(preparation.ready()).toBe(false);
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    pending.resolve(["ready"]);
    await waitFor(() => expect(preparation.ready()).toBe(true));
    expect(view.container.textContent).toBe("readyready");
  });

  it("retains same-scope results but never displays another project's late response", async () => {
    const client = {};
    const [address, setAddress] = createSignal({ key: "a:1", project: "a" });
    const second = deferred<string>();
    const third = deferred<string>();
    const load = vi.fn(({ key }: { key: string }) => key === "a:1" ? Promise.resolve("first") : key === "a:2" ? second.promise : third.promise);
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string; project: string }, string>>;
    render(() => {
      query = createSurfaceQuery({ name: "rows", source: () => ({ client, ...address() }), scope: (source) => source.project, load });
      const rows = createMemo(() => query.value() ? [{ label: query.value()! }] : []);
      return <For each={rows()}>{(row) => <input aria-label={row.label} />}</For>;
    });
    const field = await screen.findByRole("textbox", { name: "first" }) as HTMLInputElement;
    field.value = "unfinished edit";
    setAddress({ key: "a:2", project: "a" });
    expect(query.value()).toBe("first");
    expect(screen.getByRole("textbox", { name: "first" })).toBe(field);
    expect(field.value).toBe("unfinished edit");
    expect(query.displayed()?.source.key).toBe("a:1");
    setAddress({ key: "b:1", project: "b" });
    expect(query.value()).toBeUndefined();
    second.resolve("late a");
    third.resolve("new b");
    await waitFor(() => expect(query.value()).toBe("new b"));
    expect(query.displayed()?.source.project).toBe("b");
  });

  it("keeps a displayed result through refresh failure and distinguishes cold failure from empty", async () => {
    const client = {};
    const load = vi.fn<() => Promise<string[]>>().mockResolvedValueOnce(["first"]).mockRejectedValueOnce(new Error("Unavailable"));
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string }, string[]>>;
    render(() => {
      query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return null;
    });
    await waitFor(() => expect(query.value()).toEqual(["first"]));
    await query.refresh();
    expect(query.value()).toEqual(["first"]);
    expect(query.error()).toBe("Unavailable");
    expect(query.ready()).toBe(true);
  });

  it("stops idle acquisition and revalidates a retained view once on activation", async () => {
    const client = {};
    const [presence, setPresence] = createSignal<"active" | "idle">("idle");
    const load = vi.fn<() => Promise<string>>().mockResolvedValue("ready");
    const Consumer = () => {
      const query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return <span>{query.value()}</span>;
    };
    render(() => <ResidentPresenceProvider presence={presence()}><Consumer /></ResidentPresenceProvider>);
    expect(load).not.toHaveBeenCalled();
    setPresence("active");
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1));
    setPresence("idle");
    setPresence("active");
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  });

  it("keeps immutable data and pagination when a tab reactivates", async () => {
    const client = {};
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    const load = vi.fn<() => Promise<string[]>>().mockResolvedValue(["first"]);
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string }, string[]>>;
    const Consumer = () => {
      query = createSurfaceQuery({ name: "immutable", source: () => ({ client, key: "a" }), load, revalidateOnActivation: false });
      return <span>{query.value()?.join(",")}</span>;
    };
    render(() => <ResidentPresenceProvider presence={presence()}><Consumer /></ResidentPresenceProvider>);
    await waitFor(() => expect(query.value()).toEqual(["first"]));
    query.publish(["first", "second"]);
    setPresence("idle"); setPresence("active");
    await Promise.resolve();
    expect(query.value()).toEqual(["first", "second"]);
    expect(load).toHaveBeenCalledOnce();
    await query.refresh();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("revalidates cached data when a consumer is remounted", async () => {
    const client = {};
    const load = vi.fn<() => Promise<string>>().mockResolvedValueOnce("first").mockResolvedValueOnce("second");
    const Consumer = () => {
      const query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return <span>{query.value()}</span>;
    };
    const first = render(Consumer);
    await waitFor(() => expect(first.container.textContent).toBe("first"));
    first.unmount();
    const second = render(Consumer);
    expect(second.container.textContent).toBe("first");
    await waitFor(() => expect(second.container.textContent).toBe("second"));
    expect(load).toHaveBeenCalledTimes(2);
  });

  it("restores retained data when remounting after a failed refresh", async () => {
    const client = {};
    const pending = deferred<string>();
    const load = vi.fn<() => Promise<string>>().mockResolvedValueOnce("first")
      .mockRejectedValueOnce(new Error("Unavailable")).mockReturnValueOnce(pending.promise);
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string }, string>>;
    const Consumer = () => {
      query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return <span>{query.value()}</span>;
    };
    const first = render(Consumer);
    await waitFor(() => expect(query.value()).toBe("first"));
    await query.refresh();
    expect(query.error()).toBe("Unavailable");
    first.unmount();
    const reopened = render(Consumer);
    expect(reopened.container.textContent).toBe("first");
    expect(query.coldPending()).toBe(false);
    pending.resolve("updated");
    await waitFor(() => expect(reopened.container.textContent).toBe("updated"));
  });

  it("publishes a prepared empty result without reading it again on reveal", async () => {
    const client = {};
    const [presence, setPresence] = createSignal<"active" | "pending">("pending");
    const load = vi.fn<() => Promise<string[]>>().mockResolvedValue([]);
    let query!: ReturnType<typeof createSurfaceQuery<{ client: object; key: string }, string[]>>;
    const Consumer = () => {
      query = createSurfaceQuery({ name: "rows", source: () => ({ client, key: "a" }), load });
      return null;
    };
    render(() => <ResidentPresenceProvider presence={presence()}><Consumer /></ResidentPresenceProvider>);
    await waitFor(() => expect(query.value()).toEqual([]));
    setPresence("active");
    expect(query.loading()).toBe(false);
    await Promise.resolve();
    expect(load).toHaveBeenCalledTimes(1);
  });
});
