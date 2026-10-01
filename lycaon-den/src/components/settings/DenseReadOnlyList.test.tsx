import { describe, expect, it } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import {
  DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE,
  DenseReadOnlyList,
  type DenseReadOnlyListItem,
} from "./DenseReadOnlyList.tsx";

function items(count: number): DenseReadOnlyListItem[] {
  return Array.from({ length: count }, (_, i) => ({
    id: `row-${i}`,
    primary: `Primary ${i}`,
    secondary: `Secondary ${i}`,
    meta: `${i}m ago`,
  }));
}

describe("DenseReadOnlyList", () => {
  it("renders empty state when there are no items", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <DenseReadOnlyList
        items={[]}
        emptyLabel="Nothing here"
        ariaLabel="Demo list"
        testId="demo-list"
      />
    ));
    expect(getByTestId("demo-list-empty").textContent).toBe("Nothing here");
    expect(queryByTestId("demo-list-items")).toBeNull();
    expect(queryByTestId("demo-list-pager")).toBeNull();
  });

  it("renders a compact page of rows without a pager when they fit", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <DenseReadOnlyList
        items={items(3)}
        emptyLabel="Nothing here"
        ariaLabel="Demo list"
        testId="demo-list"
      />
    ));
    expect(getByTestId("demo-list-row-row-0").textContent).toContain("Primary 0");
    expect(getByTestId("demo-list-row-row-0").textContent).toContain("Secondary 0");
    expect(getByTestId("demo-list-row-row-0").textContent).toContain("0m ago");
    expect(getByTestId("demo-list-row-row-2")).toBeTruthy();
    expect(queryByTestId("demo-list-pager")).toBeNull();
  });

  it("paginates dense rows and advances with next/previous", () => {
    const total = DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE + 3;
    const { getByTestId, queryByTestId } = render(() => (
      <DenseReadOnlyList
        items={items(total)}
        emptyLabel="Nothing here"
        ariaLabel="Demo list"
        testId="demo-list"
      />
    ));

    expect(getByTestId("demo-list-pager").textContent).toContain(
      `1–${DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE} of ${total}`,
    );
    expect(getByTestId("demo-list-row-row-0")).toBeTruthy();
    expect(
      queryByTestId(`demo-list-row-row-${DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE}`),
    ).toBeNull();

    fireEvent.click(getByTestId("demo-list-pager-next"));
    expect(
      getByTestId(`demo-list-row-row-${DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE}`),
    ).toBeTruthy();
    expect(queryByTestId("demo-list-row-row-0")).toBeNull();
    expect(getByTestId("demo-list-pager").textContent).toContain(
      `${DENSE_READ_ONLY_LIST_DEFAULT_PAGE_SIZE + 1}–${total} of ${total}`,
    );

    fireEvent.click(getByTestId("demo-list-pager-prev"));
    expect(getByTestId("demo-list-row-row-0")).toBeTruthy();
  });
});
