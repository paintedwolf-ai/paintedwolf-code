{ lib, callPackage, makeRustPlatform, fetchurl, pkg-config, zstd, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
in rust.buildRustPackage {
  pname = "cargo-about";
  version = "0.9.1";
  src = fetchurl {
    url = "https://static.crates.io/crates/cargo-about/cargo-about-0.9.1.crate";
    sha256 = "4d62bfc04a579b87777727b0f5f389f72bdeb1c6cc8fbcf1c0e0c736c9b5b7a4";
  };
  cargoLock.lockFile = ./cargo-about.lock;
  postUnpack = ''cmp "$sourceRoot/Cargo.lock" ${./cargo-about.lock}'';
  buildFeatures = [ "cli" ];
  nativeBuildInputs = [ pkg-config ];
  buildInputs = [ zstd ];
  env.ZSTD_SYS_USE_PKG_CONFIG = true;
  doCheck = false;
  meta = {
    description = "Pinned offline license inventory tool for Painted Wolf Code";
    license = with lib.licenses; [ mit asl20 ];
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "cargo-about";
  };
}
