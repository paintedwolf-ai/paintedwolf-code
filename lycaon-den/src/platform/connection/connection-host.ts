import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { createLycaonClient } from "../../api/client-impl.ts";
import type { BackendConnection } from "./backend.ts";
import { noteHostInfo, type HostInfoNote } from "./host-identity.ts";

export class ConnectionHost {
  // Reactive so views that read the client before connecting re-run once it binds.
  private readonly clientSignal = createSignal<LycaonClient | null>(null);
  private currentConnection: BackendConnection | null = null;
  private currentGeneration = 0;
  private get currentClient(): LycaonClient | null { return this.clientSignal[0](); }
  private set currentClient(client: LycaonClient | null) { this.clientSignal[1](() => client); }
  get client(): LycaonClient | null { return this.currentClient; }
  get connection(): BackendConnection | null { return this.currentConnection; }
  get generation(): number { return this.currentGeneration; }
  advance(): void { this.currentGeneration++; }
  bind(connection: BackendConnection): LycaonClient {
    this.currentConnection = connection;
    this.currentClient = createLycaonClient(connection);
    return this.currentClient;
  }
  clear(): void { this.currentClient = null; this.currentConnection = null; }
  readonly setClientForTest = (client: LycaonClient | null): void => { this.currentClient = client; };
  readonly getClient = (): LycaonClient | null => this.currentClient;
  assertCurrent(generation: number, client?: LycaonClient): void {
    if (generation !== this.currentGeneration || (client && this.currentClient !== client)) {
      throw new DOMException("Backend connection changed.", "AbortError");
    }
  }
  async handshake(client: LycaonClient, generation: number): Promise<HostInfoNote> {
    const info = await client.getHost();
    this.assertCurrent(generation, client);
    return noteHostInfo(info);
  }
}
