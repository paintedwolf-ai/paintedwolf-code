import { createMemo, createRoot } from "solid-js";
import { expect, it } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { ConnectionHost } from "./connection-host.ts";

it("re-runs views that read the client before it binds", () => {
  const host = new ConnectionHost();
  const client = {} as LycaonClient;
  createRoot(dispose => {
    let runs = 0;
    const seen = createMemo(() => { runs++; return host.getClient(); });
    expect(seen()).toBeNull();
    host.setClientForTest(client);
    expect(seen()).toBe(client);
    host.setClientForTest(client);
    expect(seen()).toBe(client);
    expect(runs).toBe(2);
    host.clear();
    expect(seen()).toBeNull();
    dispose();
  });
});
