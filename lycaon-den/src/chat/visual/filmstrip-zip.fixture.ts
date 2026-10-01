/** Uncompressed filmstrip fixtures with 1×1 PNG frames. */

const TINY_PNG = Uint8Array.from([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
  0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
  0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44,
  0x41, 0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x00, 0x03, 0x00,
  0x01, 0x00, 0x05, 0xfe, 0xd4, 0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
  0x44, 0xae, 0x42, 0x60, 0x82,
]);

export function buildTestFilmstripZip(omitSecondFrame = false, frameCount = 2): Uint8Array {
  const enc = new TextEncoder();
  const frames = Array.from({ length: frameCount }, (_, index) => ({
    index,
    file: `${String(index).padStart(3, "0")}.png`,
    caption: index === 0 ? "initial" : index === 1 ? "click #load" : `Step ${index + 1}`,
  }));
  const man = enc.encode(
    JSON.stringify({
      version: 1,
      frames,
    }),
  );
  return buildStoredZip([
    ...frames.filter((frame) => !omitSecondFrame || frame.index !== 1)
      .map((frame) => ({ name: frame.file, data: TINY_PNG })),
    { name: "manifest.json", data: man },
  ]);
}

/** Packs entries without compression, as the host stores already-compressed frames. */
export function buildStoredZip(entries: ReadonlyArray<{ name: string; data: Uint8Array }>): Uint8Array {
  const enc = new TextEncoder();
  const locals: Uint8Array[] = [];
  const centrals: Uint8Array[] = [];
  let offset = 0;
  for (const e of entries) {
    const nameBytes = enc.encode(e.name);
    const local = new Uint8Array(30 + nameBytes.length + e.data.length);
    const lv = new DataView(local.buffer, local.byteOffset, local.byteLength);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(8, 0, true);
    lv.setUint32(18, e.data.length, true);
    lv.setUint32(22, e.data.length, true);
    lv.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);
    local.set(e.data, 30 + nameBytes.length);
    locals.push(local);

    const central = new Uint8Array(46 + nameBytes.length);
    const cv = new DataView(central.buffer, central.byteOffset, central.byteLength);
    cv.setUint32(0, 0x02014b50, true);
    cv.setUint16(10, 0, true);
    cv.setUint32(20, e.data.length, true);
    cv.setUint32(24, e.data.length, true);
    cv.setUint16(28, nameBytes.length, true);
    cv.setUint32(42, offset, true);
    central.set(nameBytes, 46);
    centrals.push(central);
    offset += local.length;
  }
  const cdOffset = offset;
  let cdSize = 0;
  for (const c of centrals) cdSize += c.length;
  const eocd = new Uint8Array(22);
  const ev = new DataView(eocd.buffer, eocd.byteOffset, eocd.byteLength);
  ev.setUint32(0, 0x06054b50, true);
  ev.setUint16(8, entries.length, true);
  ev.setUint16(10, entries.length, true);
  ev.setUint32(12, cdSize, true);
  ev.setUint32(16, cdOffset, true);

  const total =
    locals.reduce((n, x) => n + x.length, 0) +
    centrals.reduce((n, x) => n + x.length, 0) +
    eocd.length;
  const out = new Uint8Array(total);
  let o = 0;
  for (const part of [...locals, ...centrals, eocd]) {
    out.set(part, o);
    o += part.length;
  }
  return out;
}

/** Fixture blobs expose the byte-reading API absent from the test DOM. */
export function zipAsBlob(zip: Uint8Array, type: string): Blob {
  const copy = new Uint8Array(zip.byteLength);
  copy.set(zip);
  return Object.assign(new Blob([copy], { type }), {
    arrayBuffer: async () => copy.buffer.slice(0),
  });
}
