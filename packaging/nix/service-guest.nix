{ lib, stdenv, buildGoModule, callPackage, fetchurl, python3, gnumake
, perl, bison, flex, bash, libarchive, qemu, source }:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  lock = builtins.fromJSON (builtins.readFile (source + "/packaging/service-guest/inputs.json"));
  host = stdenv.hostPlatform.parsed.cpu.name;
  arch = { aarch64 = "arm64"; x86_64 = "amd64"; }.${host};
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  emulator = qemu.override {
    minimal = true;
    hostCpuTargets = [ "${host}-softmmu" ];
    enableBlobs = true;
    seccompSupport = true;
    pluginsSupport = false;
  };
  supervisor = (buildGoModule.override { go = tools.go; }) {
    pname = "pw-service-guest";
    inherit version;
    src = source;
    modRoot = "lycaon";
    vendorHash = import ./go-vendor-hash.nix;
    subPackages = [ "cmd/pw-service-guest" "cmd/service-guest-drivers" ];
    env.CGO_ENABLED = "0";
    ldflags = [ "-s" "-w" "-buildid=" ];
    doCheck = false;
  };
  inputs = [ lock.postgres lock.compilers."${host}-linux" lock.architectures.${arch}.base lock.architectures.${arch}.boot ];
  stage = item: let data = fetchurl { inherit (item) url sha256; }; in
    ''ln -s ${data} "$TMPDIR/cache/${item.sha256}"'';
in stdenv.mkDerivation {
  pname = "painted-wolf-private-service-userland";
  inherit version;
  dontUnpack = true;
  dontFixup = true;
  nativeBuildInputs = [ python3 gnumake perl bison flex bash libarchive ];
  passthru.runtimeQemu = emulator;
  installPhase = ''
    export HOME="$TMPDIR/home"
    mkdir -p "$HOME" "$TMPDIR/cache"
    ${lib.concatMapStringsSep "\n" stage inputs}
    python3 ${source}/scripts/service-guest/assemble.py \
      --system linux --arch ${arch} --cache "$TMPDIR/cache" \
      --driver-tool ${supervisor}/bin/service-guest-drivers --qemu-path ${emulator}/bin/qemu-system-${host} \
      --guest-binary ${supervisor}/bin/pw-service-guest --output "$TMPDIR/service-guest" || {
        log="$TMPDIR/cache/postgres-build-${host}.log"
        if test -f "$log"; then cat "$log" >&2; fi
        exit 1
      }
    mkdir -p "$out"
    cp -a "$TMPDIR/service-guest" "$out/"
  '';
}
