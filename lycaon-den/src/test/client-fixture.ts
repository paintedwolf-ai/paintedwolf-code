import type { LycaonClient } from "../api/client.ts";

export type ClientStubs = Partial<Record<keyof LycaonClient, unknown>>;

/** Creates a partial API client that rejects unstubbed properties. */
export function stubClient<T extends ClientStubs>(
  overrides: T & Record<Exclude<keyof T, keyof LycaonClient>, never>,
): LycaonClient & T;
export function stubClient(): LycaonClient;
export function stubClient(overrides: ClientStubs = {}): LycaonClient {
  return new Proxy(overrides, {
    get(target, property, receiver) {
      if (Reflect.has(target, property)) {
        return Reflect.get(target, property, receiver);
      }
      if (property === "then") return undefined;
      throw new Error(`Test client did not stub ${String(property)}`);
    },
  }) as LycaonClient;
}
