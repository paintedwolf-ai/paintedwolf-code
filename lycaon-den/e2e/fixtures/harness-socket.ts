import { mkdirSync, realpathSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { join } from "node:path";

// A socket address holds 104 bytes on macOS and 108 on Linux, terminator included.
const SOCKET_PATH_BYTES = 103;

/**
 * A socket path in the harness's short socket directory (LYCAON_E2E_SOCKET_DIR),
 * falling back to the engine's state directory. A containerised engine mounts
 * this directory at the identical path. Hosts revalidate a grant against the
 * resolved path, so the directory is resolved first.
 */
export function harnessSocketPath(name: string): string {
  const socketDir = process.env.LYCAON_E2E_SOCKET_DIR?.trim();
  const state = process.env.LYCAON_E2E_STATE_DIR?.trim();
  const directory = socketDir || (state ? join(state, "s") : "");
  if (!directory) {
    throw new Error("LYCAON_E2E_SOCKET_DIR or LYCAON_E2E_STATE_DIR is required for socket fixtures");
  }
  mkdirSync(directory, { recursive: true });
  const path = join(realpathSync(directory), name);
  const bytes = Buffer.byteLength(path);
  if (bytes > SOCKET_PATH_BYTES) {
    throw new Error(`socket path is ${bytes} bytes, over the ${SOCKET_PATH_BYTES} a socket address holds: ${path}`);
  }
  return path;
}

/** Socket approval cards retain the actual service on Linux, so the path must be listening. */
export async function listenHarnessSocket(path: string): Promise<() => Promise<void>> {
  rmSync(path, { force: true });
  const server = createServer();
  server.unref();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(path, resolve);
  });
  return () => new Promise<void>((resolve) => server.close(() => resolve()));
}
