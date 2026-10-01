import * as Y from "yjs";

// A recovered operation can depend on another window's durable, unsent edit.
// Only deliver it once every referenced identity is accepted by the host.
export function updateDependencies(update: Uint8Array): Map<number, number> {
  const { structs, ds } = Y.decodeUpdate(update);
  const included = Y.parseUpdateMeta(update);
  const required = new Map(included.from);
  const requireThrough = (client: number, clock: number) => {
    const start = included.from.get(client);
    const end = included.to.get(client);
    const needed = start != null && end != null && clock > start && clock <= end ? start : clock;
    required.set(client, Math.max(required.get(client) ?? 0, needed));
  };
  for (const item of structs) {
    if (!(item instanceof Y.Item)) continue;
    for (const reference of [item.origin, item.rightOrigin, item.parent]) {
      if (reference instanceof Y.ID) requireThrough(reference.client, reference.clock + 1);
    }
  }
  for (const [client, ranges] of ds.clients) {
    for (const range of ranges) requireThrough(client, range.clock + range.len);
  }
  return required;
}

export function dependenciesAccepted(required: ReadonlyMap<number, number>, accepted: ReadonlyMap<number, number>): boolean {
  for (const [client, clock] of required) if ((accepted.get(client) ?? 0) < clock) return false;
  return true;
}
