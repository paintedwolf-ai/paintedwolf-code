import { createContext, useContext } from "solid-js";

export interface AccordionRow {
  key: string;
  el: () => HTMLElement | null;
  /** The committed state remains open during a closing animation. */
  domOpen: () => boolean;
  apply: (next: boolean) => void;
}

export interface RowAccordion {
  register: (row: AccordionRow) => () => void;
  open: (key: string) => void;
  toggle: (key: string) => void;
  /** Retains each row’s state across virtual unmounts. */
  heldOpen?: (key: string) => boolean | undefined;
  /** A row reports each state it commits. */
  recordOpen?: (key: string, open: boolean) => void;
}

const RowAccordionContext = createContext<RowAccordion>();

export const RowAccordionProvider = RowAccordionContext.Provider;

export function useRowAccordion(): RowAccordion | undefined {
  return useContext(RowAccordionContext);
}

export function createRowAccordion(): RowAccordion {
  const rows = new Map<string, AccordionRow>();
  // DOM state remains open while a row animates closed.
  let openKey: string | null = null;
  let adoptedMountedState = false;

  const register = (row: AccordionRow): (() => void) => {
    rows.set(row.key, row);
    return () => {
      if (rows.get(row.key) !== row) return;
      rows.delete(row.key);
      if (openKey === row.key) {
        openKey = null;
        adoptedMountedState = false;
      }
    };
  };

  // Keyed remounts retain expanded state.
  const syncOpenKeyFromDom = (): void => {
    if (adoptedMountedState) return;
    adoptedMountedState = true;
    if (openKey !== null) return;
    for (const row of rows.values()) {
      if (row.domOpen()) {
        openKey = row.key;
        return;
      }
    }
  };

  const leavingRows = (key: string): AccordionRow[] => {
    const leaving: AccordionRow[] = [];
    for (const row of rows.values()) {
      if (row.key === key) continue;
      if (row.key === openKey || row.domOpen()) leaving.push(row);
    }
    return leaving;
  };

  const toggle = (key: string): void => {
    const target = rows.get(key);
    const targetEl = target?.el();
    if (!target || !targetEl) return;

    syncOpenKeyFromDom();
    const opening = openKey === key ? !target.domOpen() : true;
    const leaving = opening ? leavingRows(key) : [];
    openKey = opening ? key : null;

    for (const row of leaving) {
      row.apply(false);
    }
    target.apply(opening);
  };

  const open = (key: string): void => {
    const target = rows.get(key);
    const targetEl = target?.el();
    if (!target || !targetEl) return;

    syncOpenKeyFromDom();
    if (openKey === key && target.domOpen()) return;

    const leaving = leavingRows(key);
    openKey = key;
    for (const row of leaving) {
      row.apply(false);
    }
    target.apply(true);
  };

  return {
    register,
    open,
    toggle,
    heldOpen: (key) => (openKey ? openKey === key : undefined),
    recordOpen: (key, open) => {
      if (open) {
        openKey = key;
        adoptedMountedState = true;
      } else if (openKey === key) {
        openKey = null;
      }
    },
  };
}
