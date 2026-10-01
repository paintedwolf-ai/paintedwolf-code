import { render } from "@solidjs/testing-library";
import { createSignal, onCleanup, onMount } from "solid-js";
import { describe, expect, it } from "vitest";
import { KeyedIndex } from "./keyed-index.tsx";

type Row = { key: string; label: string };

function labels(container: HTMLElement): string[] {
  return [...container.querySelectorAll('[data-testid="row"]')].map((row) => row.textContent ?? "");
}

describe("KeyedIndex", () => {
  it("updates a row in place when content changes under a stable key", () => {
    const [rows, setRows] = createSignal<Row[]>([{ key: "a", label: "one" }]);
    let mounts = 0;
    const { container } = render(() => (
      <KeyedIndex each={rows()} keyOf={(row) => row.key}>
        {(row) => {
          onMount(() => mounts++);
          return <span data-testid="row">{row().label}</span>;
        }}
      </KeyedIndex>
    ));

    expect(labels(container)).toEqual(["one"]);
    setRows([{ key: "a", label: "two" }]);
    expect(mounts).toBe(1);
    expect(labels(container)).toEqual(["two"]);
  });

  it("mounts a fresh row for a new key", () => {
    // A mount-time branch must not see a different item.
    const [rows, setRows] = createSignal<Row[]>([{ key: "a", label: "alpha" }]);
    let mounts = 0;
    const { container } = render(() => (
      <KeyedIndex each={rows()} keyOf={(row) => row.key}>
        {(row) => {
          onMount(() => mounts++);
          const mountedKey = row().key;
          return (
            <span data-testid="row" data-mounted-key={mountedKey}>
              {row().label}
            </span>
          );
        }}
      </KeyedIndex>
    ));

    setRows([{ key: "b", label: "beta" }]);
    const row = container.querySelector('[data-testid="row"]');
    expect(mounts).toBe(2);
    expect(row?.getAttribute("data-mounted-key")).toBe("b");
    expect(row?.textContent).toBe("beta");
  });

  it("keeps a row mounted when its key moves, and moves its index with it", () => {
    // A sliding window: the list gains a row ahead and loses one behind.
    const [rows, setRows] = createSignal<Row[]>([
      { key: "a", label: "a" }, { key: "b", label: "b" }, { key: "c", label: "c" },
    ]);
    const mounted: string[] = [];
    const unmounted: string[] = [];
    const { container } = render(() => (
      <KeyedIndex each={rows()} keyOf={(row) => row.key}>
        {(row, index) => {
          const key = row().key;
          onMount(() => mounted.push(key));
          onCleanup(() => unmounted.push(key));
          return <span data-testid="row">{`${row().label}@${index()}`}</span>;
        }}
      </KeyedIndex>
    ));

    setRows([{ key: "b", label: "b" }, { key: "c", label: "c" }, { key: "d", label: "d" }]);

    expect(mounted).toEqual(["a", "b", "c", "d"]);
    expect(unmounted).toEqual(["a"]);
    expect(labels(container)).toEqual(["b@0", "c@1", "d@2"]);
  });

  it("follows a reorder without remounting", () => {
    const [rows, setRows] = createSignal<Row[]>([{ key: "a", label: "a" }, { key: "b", label: "b" }]);
    let mounts = 0;
    const { container } = render(() => (
      <KeyedIndex each={rows()} keyOf={(row) => row.key}>
        {(row) => {
          onMount(() => mounts++);
          return <span data-testid="row">{row().label}</span>;
        }}
      </KeyedIndex>
    ));

    setRows([{ key: "b", label: "b" }, { key: "a", label: "a" }]);

    expect(mounts).toBe(2);
    expect(labels(container)).toEqual(["b", "a"]);
  });

  it("renders every item when two share a key", () => {
    const rows: Row[] = [{ key: "a", label: "first" }, { key: "a", label: "second" }];
    const { container } = render(() => (
      <KeyedIndex each={rows} keyOf={(row) => row.key}>
        {(row) => <span data-testid="row">{row().label}</span>}
      </KeyedIndex>
    ));
    expect(labels(container)).toEqual(["first", "second"]);
  });
});
