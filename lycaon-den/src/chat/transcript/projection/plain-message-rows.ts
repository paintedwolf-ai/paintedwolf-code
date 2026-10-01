import { unwrap } from "solid-js/store";
import type { Message } from "../../../api/types.ts";

/**
 * The store's transcript rows as a plain array.
 *
 * The store replaces a row whole, so reading each slot is the only subscription a consumer
 * needs; projection and indexes then read plain rows instead of paying a proxy trap per field.
 * The copy is new whenever any slot changed, and its rows are the store's own objects.
 */
export function plainMessageRows(rows: readonly Message[]): Message[] {
  const raw = unwrap(rows);
  const out: Message[] = new Array(rows.length);
  for (let index = 0; index < rows.length; index++) {
    void rows[index];
    out[index] = raw[index]!;
  }
  return out;
}
