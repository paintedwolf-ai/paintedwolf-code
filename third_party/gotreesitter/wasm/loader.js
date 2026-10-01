// Shared loader for the gotreesitter runtime and grammargen WASM builds.
// Use the wasm_exec.js from the same Go toolchain that built the .wasm file.
// Usage:
//   <script src="wasm_exec.js"></script>
//   <script src="loader.js"></script>
//   <script>
//     loadGotreesitter("gotreesitter.wasm").then(() => {
//       // gotreesitter now exposes the API for the selected build.
//     });
//   </script>

async function loadGotreesitter(wasmPath) {
  const go = new Go();
  let result;
  if (typeof WebAssembly.instantiateStreaming === "function") {
    result = await WebAssembly.instantiateStreaming(fetch(wasmPath), go.importObject);
  } else {
    const resp = await fetch(wasmPath);
    const bytes = await resp.arrayBuffer();
    result = await WebAssembly.instantiate(bytes, go.importObject);
  }
  go.run(result.instance);
  // gotreesitter is now available as a global
  return window.gotreesitter;
}
