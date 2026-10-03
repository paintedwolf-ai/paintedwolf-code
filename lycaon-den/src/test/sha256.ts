// Fixtures also load in the browser harness, where node:crypto is unavailable
// and WebCrypto digests are asynchronous.
const ROUND = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

const at = (words: Uint32Array, index: number) => words[index] ?? 0;
const rotr = (value: number, bits: number) => (value >>> bits) | (value << (32 - bits));

/** Hex SHA-256 of the text's UTF-8 bytes. */
export function sha256Hex(text: string): string {
  const bytes = new TextEncoder().encode(text);
  const padded = new Uint8Array(Math.ceil((bytes.length + 9) / 64) * 64);
  padded.set(bytes);
  padded[bytes.length] = 0x80;
  const view = new DataView(padded.buffer);
  const bits = bytes.length * 8;
  view.setUint32(padded.length - 8, Math.floor(bits / 2 ** 32));
  view.setUint32(padded.length - 4, bits >>> 0);
  const state = new Uint32Array([
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
  ]);
  const schedule = new Uint32Array(64);
  for (let offset = 0; offset < padded.length; offset += 64) {
    for (let i = 0; i < 16; i++) schedule[i] = view.getUint32(offset + i * 4);
    for (let i = 16; i < 64; i++) {
      const early = at(schedule, i - 15);
      const late = at(schedule, i - 2);
      const s0 = rotr(early, 7) ^ rotr(early, 18) ^ (early >>> 3);
      const s1 = rotr(late, 17) ^ rotr(late, 19) ^ (late >>> 10);
      schedule[i] = at(schedule, i - 16) + s0 + at(schedule, i - 7) + s1;
    }
    const work = state.slice();
    for (let i = 0; i < 64; i++) {
      const e = at(work, 4);
      const a = at(work, 0);
      const s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const choose = (e & at(work, 5)) ^ (~e & at(work, 6));
      const t1 = at(work, 7) + s1 + choose + at(ROUND, i) + at(schedule, i);
      const s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const majority = (a & at(work, 1)) ^ (a & at(work, 2)) ^ (at(work, 1) & at(work, 2));
      work.copyWithin(1, 0, 7);
      work[4] = at(work, 4) + t1;
      work[0] = t1 + s0 + majority;
    }
    for (let i = 0; i < 8; i++) state[i] = at(state, i) + at(work, i);
  }
  return Array.from(state, (word) => word.toString(16).padStart(8, "0")).join("");
}
