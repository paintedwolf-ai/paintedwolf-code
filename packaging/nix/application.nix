{ lib, stdenvNoCC, callPackage, python3, makeWrapper, wrapGAppsHook3, gtk3, apparmor-parser, apparmor-profiles, mesa
, gsettings-desktop-schemas, glib-networking, nix, source, shell, engine
, bialy, document-core, resources
, desktopSettings ? callPackage ./desktop-settings.nix { inherit source; }
, scannerTools ? callPackage ./scanner-tools.nix { inherit source; }
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
    mkdir -p "$root/bin" "$out/bin" "$out/share/applications" "$out/share/icons/hicolor/128x128/apps" \
      "$out/share/polkit-1/actions"
    cp -r ${resources}/engine-root "$root/"
    cp ${resources}/THIRD-PARTY-NOTICES.md "$root/"
    cp ${source}/VERSION "$root/release-version"
    install -m755 ${shell}/bin/painted-wolf-code "$root/bin/painted-wolf-code"
    install -m755 ${desktopSettings}/bin/pw-desktop-settings "$root/bin/pw-desktop-settings"
    for executable in pw pw-logs pw-vault-identity; do
      install -m755 ${engine}/bin/$executable "$root/bin/$executable"
    done
    install -m755 ${bialy}/bin/bialy "$root/bin/bialy"
    install -m755 ${document-core}/bin/pw-document-core "$root/bin/pw-document-core"
    # Outside NixOS no driver environment is selected by the system configuration, and
    # host-installed drivers are foreign to the generation, so the closure's Mesa is the
    # explicit EGL, GBM and Vulkan driver. An explicitly set variable is kept.
    for driver in ${mesa}/share/glvnd/egl_vendor.d/50_mesa.json ${mesa}/lib/gbm ${mesa}/lib/dri; do
      test -e "$driver"
    done
    vulkan_drivers=$(printf '%s:' ${mesa}/share/vulkan/icd.d/*.json)
    test -e "''${vulkan_drivers%%:*}"
    graphics='[ -e /run/opengl-driver ] || {
      export __EGL_VENDOR_LIBRARY_DIRS="''${__EGL_VENDOR_LIBRARY_DIRS:-${mesa}/share/glvnd/egl_vendor.d}"
      export GBM_BACKENDS_PATH="''${GBM_BACKENDS_PATH:-${mesa}/lib/gbm}"
      export LIBGL_DRIVERS_PATH="''${LIBGL_DRIVERS_PATH:-${mesa}/lib/dri}"
      export VK_DRIVER_FILES="''${VK_DRIVER_FILES:-'"''${vulkan_drivers%:}"'}"
    }'
    for executable in painted-wolf-code pw pw-logs; do
      makeWrapper "$root/bin/$executable" "$out/bin/$executable" --run "$graphics"
    done
    install -m644 ${source}/lycaon-den/src-tauri/icons/128x128.png "$out/share/icons/hicolor/128x128/apps/painted-wolf-code.png"
    substitute ${source}/packaging/linux/dev.paintedwolf.code.desktop \
      "$out/share/applications/dev.paintedwolf.code.desktop" \
      --replace-fail /opt/painted-wolf-code/bin/painted-wolf-code "$out/bin/painted-wolf-code"
    install -m644 ${source}/packaging/linux/polkit/dev.paintedwolf.code.policy \
      "$out/share/polkit-1/actions/dev.paintedwolf.code.policy"
    runHook postInstall
  '';
  postFixup = ''
    ${scannerTools}/bin/opengrep-artifact -mode audit \
      -artifact-directory ${resources.inputs.scannerOriginal}/artifact \
      -packaged-directory "$out/lib/painted-wolf-code/engine-root/bundled/opengrep-${resources.inputs.pins.scannerVersion}" \
      -consumer-identity-file ${resources}/scanner-identity.base64 \
      -evidence-root ${source}/lycaon/config/runtime/scanners \
      -audit-output "$out/lib/painted-wolf-code/scanner-audit.json" > /dev/null
    (cd ${source} && python3 -m scripts.linux_release.nix_layout \
      --output "$out" --architecture ${lib.escapeShellArg architecture} \
      --version ${lib.escapeShellArg version} --nix-store ${nix}/bin/nix-store)
    # The shipped engine grant must parse; administrators load it unchanged.
    for profile in "$out"/share/painted-wolf-code/apparmor/*; do
      ${apparmor-parser}/bin/apparmor_parser --skip-kernel-load --skip-cache --skip-read-cache \
        -I ${apparmor-profiles}/etc/apparmor.d "$profile"
    done
  '';
  meta = {
    description = "Painted Wolf Code desktop application and complete immutable runtime resources";
    license = lib.licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "painted-wolf-code";
  };
}
