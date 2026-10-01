type PaginationWindow = {
  page: number;
  total: number;
  pageCount: number;
  from: number;
  to: number;
};

export function paginationWindow(
  total: number,
  page: number,
  pageSize: number,
): PaginationWindow {
  if (total <= 0 || pageSize <= 0) {
    return { page: 0, total: 0, pageCount: 0, from: 0, to: 0 };
  }
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const clampedPage = Math.min(Math.max(page, 0), pageCount - 1);
  const offset = clampedPage * pageSize;
  return {
    page: clampedPage,
    total,
    pageCount,
    from: offset + 1,
    to: Math.min(offset + pageSize, total),
  };
}

export function paginateSlice<T>(
  rows: readonly T[],
  page: number,
  pageSize: number,
): PaginationWindow & { slice: T[] } {
  const window = paginationWindow(rows.length, page, pageSize);
  if (window.pageCount === 0) {
    return { ...window, slice: [] };
  }
  const offset = window.page * pageSize;
  return { ...window, slice: rows.slice(offset, offset + pageSize) };
}
