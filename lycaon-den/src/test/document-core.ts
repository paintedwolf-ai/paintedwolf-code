import { readFile } from "node:fs/promises";
import { WASI } from "node:wasi";

type CoreRequest = {
  action: string;
  handle: number;
  client?: number;
  update?: string;
  vector?: string;
  edits?: { index: number; delete: number; insert: string }[];
  checkpoint?: boolean;
};

type CoreResponse = {
  text: string;
  vector: string;
  update: string;
  checkpoint: string;
  error?: string;
};

/** Runs the host's actual embedded binary, with no filesystem or network imports. */
export async function documentCore() {
  const wasi = new WASI({ version: "preview1", args: [], env: {}, preopens: {} });
  const bytes = await readFile(new URL("../../../lycaon/internal/documentcore/core.wasm", import.meta.url));
  const { instance } = await WebAssembly.instantiate(bytes, wasi.getImportObject() as WebAssembly.Imports);
  wasi.initialize(instance);
  const exports = instance.exports as {
    memory: WebAssembly.Memory;
    allocate(length: number): number;
    release(pointer: number, length: number): void;
    execute_request(pointer: number, length: number): bigint;
  };
  return (request: CoreRequest): CoreResponse => {
    const input = new TextEncoder().encode(JSON.stringify(request));
    const pointer = exports.allocate(input.length);
    new Uint8Array(exports.memory.buffer, pointer, input.length).set(input);
    const result = exports.execute_request(pointer, input.length);
    const address = Number(result >> 32n);
    const length = Number(result & 0xffffffffn);
    const response = JSON.parse(new TextDecoder().decode(new Uint8Array(exports.memory.buffer, address, length))) as CoreResponse;
    exports.release(pointer, input.length);
    exports.release(address, length);
    return response;
  };
}
