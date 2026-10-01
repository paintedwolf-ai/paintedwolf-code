/** Nonce the packaged CSP requires on every runtime-created `<style>`. */
export function documentStyleNonce(): string | null {
  if (typeof document === "undefined") return null;
  for (const el of document.querySelectorAll<HTMLStyleElement>("style")) {
    if (el.nonce) return el.nonce;
  }
  return null;
}
