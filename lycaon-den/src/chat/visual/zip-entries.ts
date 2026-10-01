/** Reads an in-memory ZIP archive: every entry, or one entry by name without touching the rest. */

type ZipEntry = {
  name: string;
  method: number;
  data: Uint8Array;
};

const EOCD_SIGNATURE = 0x06054b50;
const CENTRAL_SIGNATURE = 0x02014b50;
const LOCAL_SIGNATURE = 0x04034b50;

/** Walks the central directory, yielding each entry's compressed bytes as it goes. */
function* zipEntries(buf: Uint8Array): Generator<ZipEntry> {
  const view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  let eocd = -1;
  for (let i = buf.length - 22; i >= 0; i--) {
    if (view.getUint32(i, true) === EOCD_SIGNATURE) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) {
    throw new Error("zip: EOCD not found");
  }
  const cdOffset = view.getUint32(eocd + 16, true);
  const cdEntries = view.getUint16(eocd + 10, true);
  let off = cdOffset;
  for (let n = 0; n < cdEntries; n++) {
    if (view.getUint32(off, true) !== CENTRAL_SIGNATURE) {
      throw new Error("zip: bad central directory");
    }
    const method = view.getUint16(off + 10, true);
    const compSize = view.getUint32(off + 20, true);
    const nameLen = view.getUint16(off + 28, true);
    const extraLen = view.getUint16(off + 30, true);
    const commentLen = view.getUint16(off + 32, true);
    const localOff = view.getUint32(off + 42, true);
    const name = new TextDecoder().decode(buf.subarray(off + 46, off + 46 + nameLen));
    off += 46 + nameLen + extraLen + commentLen;

    if (view.getUint32(localOff, true) !== LOCAL_SIGNATURE) {
      throw new Error(`zip: bad local header for ${name}`);
    }
    const lNameLen = view.getUint16(localOff + 26, true);
    const lExtraLen = view.getUint16(localOff + 28, true);
    const dataStart = localOff + 30 + lNameLen + lExtraLen;
    yield { name, method, data: buf.subarray(dataStart, dataStart + compSize) };
  }
}

async function decodeEntry(entry: ZipEntry): Promise<Uint8Array> {
  if (entry.method === 0) return entry.data;
  if (entry.method === 8) return inflateRaw(entry.data);
  throw new Error(`zip: unsupported method ${entry.method} for ${entry.name}`);
}

/** Every entry of the archive, decoded; deflated entries use the browser's decompression stream. */
export async function readZipEntries(buf: Uint8Array): Promise<Map<string, Uint8Array>> {
  const out = new Map<string, Uint8Array>();
  for (const entry of zipEntries(buf)) {
    out.set(entry.name, await decodeEntry(entry));
  }
  return out;
}

/** One entry by name, decoded; the other entries are not read. */
export async function readZipEntry(buf: Uint8Array, name: string): Promise<Uint8Array | undefined> {
  for (const entry of zipEntries(buf)) {
    if (entry.name === name) return decodeEntry(entry);
  }
  return undefined;
}

async function inflateRaw(data: Uint8Array): Promise<Uint8Array> {
  if (typeof DecompressionStream === "undefined") {
    throw new Error("zip deflate requires DecompressionStream");
  }
  // ZIP entries contain raw deflate without a zlib header.
  const ds = new DecompressionStream("deflate-raw");
  const writer = ds.writable.getWriter();
  const reader = ds.readable.getReader();
  const writeP = writer.write(new Uint8Array(data)).then(() => writer.close());
  const chunks: Uint8Array[] = [];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (value) chunks.push(value);
  }
  await writeP;
  let total = 0;
  for (const c of chunks) total += c.length;
  const out = new Uint8Array(total);
  let o = 0;
  for (const c of chunks) {
    out.set(c, o);
    o += c.length;
  }
  return out;
}
