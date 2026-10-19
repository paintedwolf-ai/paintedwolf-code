{ lib, stdenvNoCC, python3, makeWrapper, wrapGAppsHook3, gtk3
, gsettings-desktop-schemas, glib-networking, nix, source, shell, engine
, bialy, document-core, resources
}:
let
  version = lib.strings.trim (builtins.readFile (source + "/VERSION"));
  architecture = stdenvNoCC.hostPlatform.parsed.cpu.name;
in
assert lib.assertMsg engine.scannerIdentitySupplied
  "The final application requires an engine bound to the independently qualified final scanner";
stdenvNoCC.mkDerivation {
  pname = "painted-wolf-code";
  inherit version;
  dontUnpack = true;
  nativeBuildInputs = [ python3 makeWrapper wrapGAppsHook3 ];
  buildInputs = [ gtk3 gsettings-desktop-schemas glib-networking ];
  dontStrip = true;
  dontPatchELF = true;
  dontPatchShebangs = true;
  installPhase = ''
    runHook preInstall
    root="$out/lib/painted-wolf-code"
    mkdir -p "$root/bin" "$out/bin" "$out/share/applications" "$out/share/icons/hicolor/128x128/apps"
    cp -r ${resources}/engine-root "$root/"
    cp ${resources}/THIRD-PARTY-NOTICES.md "$root/"
    cp ${source}/VERSION "$root/release-version"
    install -m755 ${shell}/bin/painted-wolf-code "$root/bin/painted-wolf-code"
    for executable in pw pw-logs pw-vault-identity; do
      install -m755 ${engine}/bin/$executable "$root/bin/$executable"
    done
    install -m755 ${bialy}/bin/bialy "$root/bin/bialy"
    install -m755 ${document-core}/bin/pw-document-core "$root/bin/pw-document-core"
    for executable in painted-wolf-code pw pw-logs; do
      makeWrapper "$root/bin/$executable" "$out/bin/$executable"
    done
    install -m644 ${source}/lycaon-den/src-tauri/icons/128x128.png "$out/share/icons/hicolor/128x128/apps/painted-wolf-code.png"
    cat > "$out/share/applications/painted-wolf-code.desktop" <<EOF
    [Desktop Entry]
    Type=Application
    Name=Painted Wolf Code
    Exec=$out/bin/painted-wolf-code %U
    Icon=painted-wolf-code
    Terminal=false
    Categories=Development;IDE;
    StartupWMClass=dev.paintedwolf.code
    MimeType=x-scheme-handler/paintedwolf;
    EOF
    runHook postInstall
  '';
  postFixup = ''
    python3 ${source}/scripts/linux_release/nix_layout.py \
      --output "$out" --architecture ${lib.escapeShellArg architecture} \
      --version ${lib.escapeShellArg version} --nix-store ${nix}/bin/nix-store
  '';
  meta = {
    description = "Painted Wolf Code desktop application and complete immutable runtime resources";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "painted-wolf-code";
  };
}
