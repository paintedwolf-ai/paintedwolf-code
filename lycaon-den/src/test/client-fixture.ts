import { vi } from "vitest";
import type { LycaonClient } from "../api/client.ts";
import { observeOperationContract } from "../api/operation-contract.ts";

export type ClientStubs = Partial<Record<keyof LycaonClient, unknown>>;

type Method = (...args: unknown[]) => unknown;
const { createLycaonClient } = await vi.importActual<typeof import("../api/client-impl.ts")>("../api/client-impl.ts");
const probeClient = createLycaonClient({ baseUrl: "http://probe.invalid", apiToken: "" });
const originals = new WeakMap<Method, Method>();
const wrappers = new WeakMap<Method, Map<PropertyKey, Method>>();

export function stubbedFunction(value: unknown): unknown {
  return typeof value === "function" ? originals.get(value as Method) ?? value : value;
}

function probeMethod(property: PropertyKey, override: Method): Method {
  const real = Reflect.get(probeClient, property) as unknown;
  if (typeof real !== "function") return override;
  const original = originals.get(override) ?? override;
  const methods = wrappers.get(original) ?? new Map<PropertyKey, Method>();
  const existing = methods.get(property);
  if (existing) return existing;

  const wrapped = new Proxy(original, {
    apply(target, thisArg, args) {
      const violation = observeOperationContract(() => {
        try {
          const result: unknown = Reflect.apply(real, probeClient, args);
          void Promise.resolve(result).catch(() => undefined);
        } catch {
          // Some fixture arguments cannot build a request.
        }
      });
      if (violation) throw violation;
      return Reflect.apply(target, thisArg, args);
    },
  });
  originals.set(wrapped, original);
  methods.set(property, wrapped);
  wrappers.set(original, methods);
  return wrapped;
}

/** Creates a partial API client that rejects unstubbed properties. */
export function stubClient<T extends ClientStubs>(
  overrides: T & Record<Exclude<keyof T, keyof LycaonClient>, never>,
): LycaonClient & T;
export function stubClient(): LycaonClient;
export function stubClient(overrides: ClientStubs = {}): LycaonClient {
  return new Proxy(overrides, {
    get(target, property, receiver) {
      if (Reflect.has(target, property)) {
        const value: unknown = Reflect.get(target, property, receiver);
        return typeof value === "function" ? probeMethod(property, value as Method) : value;
      }
      if (property === "then") return undefined;
      throw new Error(`Test client did not stub ${String(property)}`);
    },
  }) as LycaonClient;
}
