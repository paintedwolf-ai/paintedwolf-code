{ lib, stdenvNoCC, callPackage, makeRustPlatform, buildGoModule, go-licenses
, source, frontend
, inputs ? callPackage ./resource-inputs.nix { inherit source; }
}:
let
  tools = callPackage ./toolchains.nix { inherit source; };
  rust = makeRustPlatform { cargo = tools.rust; rustc = tools.rust; };
  cargoAbout = callPackage ./notice-tools.nix { inherit source; };
  goLicenses = go-licenses.override {
    go = tools.go;
    buildGoModule = buildGoModule.override { go = tools.go; };
  };
  engine = callPackage ./engine.nix { inherit source; };
  goInventory = engine.overrideAttrs (_: {
    pname = "painted-wolf-code-go-notices";
    nativeBuildInputs = [ goLicenses ];
    preBuild = "";
    buildPhase = ''
      runHook preBuild
      packages=(./cmd/lycaon ./cmd/pw-logs ./cmd/pw-vault-identity)
      ignored=(--ignore=github.com/lycaon/lycaon --ignore=github.com/deitch/magic
        --ignore=github.com/alecthomas/chroma/v2 --ignore=github.com/spdx/tools-golang)
      go-licenses check "''${packages[@]}" "''${ignored[@]}" --disallowed_types=forbidden,restricted,unknown
      go-licenses csv "''${packages[@]}" --ignore=github.com/lycaon/lycaon > go.csv
      go-licenses save "''${packages[@]}" "''${ignored[@]}" --save_path=go-save --force
      runHook postBuild
    '';
    installPhase = ''
      mkdir -p "$out"
      cp go.csv "$out/"
      cp -R go-save "$out/"
    '';
    postInstall = "";
  });
  npmInventory = frontend.overrideAttrs (_: {
    pname = "painted-wolf-code-npm-notices";
    buildPhase = ''bun scripts/licenses-npm-notices.ts --out npm.md'';
    installPhase = ''mkdir -p "$out"; cp npm.md "$out/"'';
    dontFixup = true;
  });
  cratePaths = {
    crates = "lycaon-den/src-tauri";
    document-crates = "lycaon/internal/documentcore/native";
    decide-crates = "lycaon/internal/decide/native";
  };
  crateVendors = lib.mapAttrs (_: path: rust.importCargoLock {
    lockFile = source + "/${path}/Cargo.lock";
  }) cratePaths;
  rustInventory = stdenvNoCC.mkDerivation {
    pname = "painted-wolf-code-rust-notices";
    version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
    src = source;
    nativeBuildInputs = [ tools.rust cargoAbout ];
    env.CARGO_NET_OFFLINE = "true";
    dontConfigure = true;
    dontBuild = true;
    installPhase = ''
      mkdir -p "$out"
      export CARGO_HOME="$TMPDIR/cargo-home"
      mkdir -p "$CARGO_HOME"
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (name: path: ''
        mkdir -p ${path}/.cargo
        cat > ${path}/.cargo/config.toml <<'CARGO_CONFIG'
        [source.crates-io]
        replace-with = "vendored-sources"
        [source.vendored-sources]
        directory = "${crateVendors.${name}}"
        CARGO_CONFIG
        (
          cd ${path}
          cargo-about generate --frozen --fail --format json \
            --config ${source}/lycaon-den/src-tauri/about.toml \
            ${lib.optionalString (name == "decide-crates") ''--features mlx,vulkan''} \
            -o "$out/${name}.json"
        )
      '') cratePaths)}
    '';
  };
in stdenvNoCC.mkDerivation {
  pname = "painted-wolf-code-third-party-notices";
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  src = source;
  nativeBuildInputs = [ tools.bun ];
  env.SOURCE_DATE_EPOCH = toString (source.lastModified or 1);
  dontConfigure = true;
  dontBuild = true;
  postPatch = ''
    substituteInPlace scripts/licenses-assemble.ts \
      --replace-fail 'new Date().toISOString()' 'new Date(Number(process.env.SOURCE_DATE_EPOCH) * 1000).toISOString()'
  '';
  installPhase = ''
    mkdir -p "$out"
    bun scripts/licenses-assemble.ts \
      --go-csv ${goInventory}/go.csv --go-save ${goInventory}/go-save \
      --npm-md ${npmInventory}/npm.md \
      --crates-json ${rustInventory}/crates.json \
      --document-crates-json ${rustInventory}/document-crates.json \
      --decide-crates-json ${rustInventory}/decide-crates.json \
      --opengrep-artifact ${inputs.scannerOriginal}/artifact \
      --out "$out/THIRD-PARTY-NOTICES.md"
  '';
  passthru = { inherit goInventory npmInventory rustInventory cargoAbout; };
  meta = {
    description = "Offline complete third-party notices for Painted Wolf Code";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
  };
}
