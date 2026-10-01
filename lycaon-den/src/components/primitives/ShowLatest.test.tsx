import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { Show, createSignal, onMount } from "solid-js";
import type { Accessor } from "solid-js";
import { ShowLatest } from "./ShowLatest.tsx";

type Subject = { id: string; label: string };

function refreshed(id: string, label: string): Subject {
  return { id, label };
}

describe("ShowLatest", () => {
  it("keeps children mounted while the object behind one identity is replaced", () => {
    const [subject, setSubject] = createSignal<Subject | undefined>(
      refreshed("a", "first"),
    );
    let mounts = 0;
    const { container } = render(() => (
      <ShowLatest when={subject()} by={(s) => s.id}>
        {(s) => {
          onMount(() => {
            mounts += 1;
          });
          return <span data-testid="label">{s().label}</span>;
        }}
      </ShowLatest>
    ));

    expect(mounts).toBe(1);
    setSubject(refreshed("a", "second"));
    expect(container.textContent).toBe("second");
    expect(mounts).toBe(1);
  });

  it("remounts when the identity itself changes", () => {
    const [subject, setSubject] = createSignal<Subject | undefined>(
      refreshed("a", "first"),
    );
    let mounts = 0;
    render(() => (
      <ShowLatest when={subject()} by={(s) => s.id}>
        {(s) => {
          onMount(() => {
            mounts += 1;
          });
          return <span>{s().label}</span>;
        }}
      </ShowLatest>
    ));

    setSubject(refreshed("b", "other"));
    expect(mounts).toBe(2);
  });

  it("treats presence as the identity when `by` is omitted", () => {
    const [draft, setDraft] = createSignal<Subject | undefined>(
      refreshed("a", "one"),
    );
    let mounts = 0;
    const { container } = render(() => (
      <ShowLatest when={draft()}>
        {(d) => {
          onMount(() => {
            mounts += 1;
          });
          return <span>{d().label}</span>;
        }}
      </ShowLatest>
    ));

    // Object replacement preserves the mounted child.
    setDraft(refreshed("b", "two"));
    setDraft(refreshed("c", "three"));
    expect(container.textContent).toBe("three");
    expect(mounts).toBe(1);

    // Returning from absence mounts a fresh child.
    setDraft(undefined);
    setDraft(refreshed("d", "four"));
    expect(mounts).toBe(2);
  });

  /** Teardown can read the last value after the source disappears. */
  it("retains the last value when the source becomes absent", () => {
    const [subject, setSubject] = createSignal<Subject | undefined>(
      refreshed("a", "kept"),
    );
    let captured: Accessor<Subject> | undefined;
    render(() => (
      <ShowLatest when={subject()} by={(s) => s.id}>
        {(s) => {
          captured = s;
          return <span>{s().label}</span>;
        }}
      </ShowLatest>
    ));

    setSubject(undefined);
    expect(captured).toBeDefined();
    expect(() => captured?.()).not.toThrow();
    expect(captured?.().label).toBe("kept");
  });

  it.each([false, true])("retains unread values through teardown (refresh=%s)", (refresh) => {
    const initial = refreshed("a", "kept");
    const [subject, setSubject] = createSignal<Subject | undefined>(initial);
    let captured: Accessor<Subject> | undefined;
    render(() => (
      <ShowLatest when={subject()} by={(s) => s.id}>
        {(s) => {
          captured = s;
          return <span />;
        }}
      </ShowLatest>
    ));

    const expected = refresh ? refreshed("a", "updated") : initial;
    if (refresh) setSubject(expected);
    setSubject(undefined);
    expect(captured?.()).toEqual(expected);
  });

  it("keeps retained accessors bound to their mounted subject", () => {
    const [subject, setSubject] = createSignal(refreshed("a", "first"));
    const captured: Accessor<Subject>[] = [];
    render(() => (
      <ShowLatest when={subject()} by={(s) => s.id}>
        {(s) => {
          captured.push(s);
          return <span>{s().label}</span>;
        }}
      </ShowLatest>
    ));

    setSubject(refreshed("b", "second"));
    expect(captured).toHaveLength(2);
    expect(captured[0]?.()).toEqual(refreshed("a", "first"));
    expect(captured[1]?.()).toEqual(refreshed("b", "second"));

    setSubject(refreshed("a", "third"));
    expect(captured).toHaveLength(3);
    expect(captured[0]?.()).toEqual(refreshed("a", "first"));
    expect(captured[2]?.()).toEqual(refreshed("a", "third"));
  });

  it("non-keyed callbacks expire when their source becomes absent", () => {
    const [subject, setSubject] = createSignal<Subject | undefined>(
      refreshed("a", "kept"),
    );
    let captured: Accessor<Subject> | undefined;
    render(() => (
      <Show when={subject()}>
        {(s) => {
          captured = s;
          return <span>{s().label}</span>;
        }}
      </Show>
    ));

    setSubject(undefined);
    expect(captured).toBeDefined();
    expect(() => captured?.()).toThrow();
  });

  it("mounts on a falsy key and renders the fallback only when absent", () => {
    const [n, setN] = createSignal<number | undefined>(0);
    const { container } = render(() => (
      <ShowLatest when={n()} by={(v) => v} fallback={<span>none</span>}>
        {(v) => <span>value {v()}</span>}
      </ShowLatest>
    ));

    expect(container.textContent).toBe("value 0");
    setN(undefined);
    expect(container.textContent).toBe("none");
  });
});
