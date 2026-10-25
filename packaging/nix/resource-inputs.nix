{ lib, stdenv, stdenvNoCC, fetchurl, autoPatchelfHook, unzip, python3, binutils
, glib, nss, nspr, at-spi2-core, dbus, libX11, libXcomposite, libXdamage
, libXext, libXfixes, libXrandr, libgbm, expat, libxcb, libxkbcommon, udev
, alsa-lib, fontconfig, source
}:
let
  pins = import ./resource-pins.nix { inherit lib source; system = stdenv.hostPlatform.system; };
  gitArchive = fetchurl {
    url = "https://github.com/paintedwolf-ai/paintedwolf-git/releases/download/v${pins.gitVersion}+${pins.gitBuild}/paintedwolf-git-${pins.gitVersion}+${pins.gitBuild}-${pins.platform}.tar.gz";
    sha256 = pins.gitSHA256;
  };
  ocrArchive = fetchurl {
    url = "https://github.com/paintedwolf-ai/paintedwolf-tesseract/releases/download/v${pins.ocrVersion}/paintedwolf-tesseract-${pins.ocrVersion}-${pins.platform}.tar.gz";
    sha256 = pins.ocrSHA256;
  };
  scannerArchive = fetchurl {
    url = "https://github.com/paintedwolf-ai/paintedwolf-opengrep/releases/download/v${pins.scannerVersion}/opengrep-${pins.scannerVersion}-${pins.platform}.tar.gz";
    sha256 = pins.scannerSHA256;
  };
  headFiles = lib.mapAttrs (name: pin: fetchurl {
    url = "https://huggingface.co/${pins.decision.source.heads.repo}/resolve/${pins.decision.source.heads.revision}/${name}.safetensors";
    sha256 = pin.sha256;
  }) pins.decision.heads;
  modelFiles = lib.mapAttrs (path: sha256: fetchurl {
    url = "https://huggingface.co/${pins.model.id}/resolve/${pins.model.revision}/${path}";
    inherit sha256;
  }) pins.model.files;
  ocrDataFiles = lib.mapAttrs (language: sha256: fetchurl {
    url = "https://raw.githubusercontent.com/${pins.ocrData.repository}/${pins.ocrData.revision}/${language}.traineddata";
    inherit sha256;
  }) pins.ocrData.files;
in {
  inherit pins scannerArchive;
  browser = stdenv.mkDerivation {
    pname = "painted-wolf-code-browser";
    version = pins.browserVersion;
    src = fetchurl {
      url = "https://storage.googleapis.com/chrome-for-testing-public/${pins.browserVersion}/${pins.chromePlatform}/chrome-headless-shell-${pins.chromePlatform}.zip";
      sha256 = pins.browserSHA256;
    };
    nativeBuildInputs = [ unzip autoPatchelfHook ];
    buildInputs = [ glib nss nspr at-spi2-core dbus libX11 libXcomposite libXdamage
      libXext libXfixes libXrandr libgbm expat libxcb libxkbcommon udev alsa-lib
      fontconfig stdenv.cc.cc.lib ];
    dontConfigure = true;
    dontBuild = true;
    dontStrip = true;
    installPhase = ''
      runHook preInstall
      mkdir -p "$out"
      cp -r . "$out/browser"
      chmod -R u+w "$out/browser"
      chmod 0755 "$out/browser/chrome-headless-shell"
      addAutoPatchelfSearchPath "$out/browser"
      runHook postInstall
    '';
  };
  gitengine = stdenv.mkDerivation {
    pname = "painted-wolf-code-gitengine";
    version = "${pins.gitVersion}+${pins.gitBuild}";
    src = gitArchive;
    unpackPhase = ''
      runHook preUnpack
      mkdir source
      tar -xzf "$src" -C source
      sourceRoot=source
      runHook postUnpack
    '';
    nativeBuildInputs = [ autoPatchelfHook ];
    buildInputs = [ stdenv.cc.cc.lib ];
    dontConfigure = true;
    dontBuild = true;
    installPhase = ''
      runHook preInstall
      mkdir -p "$out/gitengine"
      cp -a ./. "$out/gitengine/"
      core="$out/gitengine/libexec/git-core"
      test -x "$core/git"
      test -x "$core/git-remote-http"
      rm -rf "$out/gitengine/share"
      find "$core" -maxdepth 1 -type l -lname git -delete
      for helper in "$core"/git-*; do
        case "$(basename "$helper")" in git-lfs|git-remote-http) continue ;; esac
        if test -f "$helper" && cmp -s "$helper" "$core/git"; then rm -f "$helper"; fi
      done
      rm -f "$core/git-remote-https"
      cp "$core/git-remote-http" "$core/git-remote-https"
      if test -e "$core/git-lfs"; then cp -L "$core/git-lfs" git-lfs; else cp -L "$out/gitengine/bin/git-lfs" git-lfs; fi
      rm -f "$out/gitengine/bin/git-lfs" "$core/git-lfs" "$out/gitengine/bin/git"
      install -m0755 git-lfs "$out/gitengine/bin/git-lfs"
      ln -s ../libexec/git-core/git "$out/gitengine/bin/git"
      printf '%s\n' ${lib.escapeShellArg pins.gitVersion} > "$out/gitengine/VERSION"
      printf '%s\n' ${lib.escapeShellArg pins.gitSHA256} > "$out/gitengine/ARCHIVE_SHA256"
      runHook postInstall
    '';
  };
  ocr = stdenvNoCC.mkDerivation {
    pname = "painted-wolf-code-ocr";
    version = pins.ocrVersion;
    dontUnpack = true;
    dontFixup = true;
    nativeBuildInputs = [ binutils ];
    installPhase = ''
      runHook preInstall
      mkdir trained-data
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (language: file: ''cp ${file} trained-data/${language}.traineddata'') ocrDataFiles)}
      OCRENGINE_DEST="$out/ocr" bash ${source}/scripts/ocrengine-fetch.sh \
        --target ${pins.target} --archive ${ocrArchive} --trained-data-dir "$PWD/trained-data"
      if readelf -l "$out/ocr/bin/tesseract" | grep -q INTERP; then
        echo "Refusing a nonstatic pinned OCR engine" >&2; exit 1
      fi
      runHook postInstall
    '';
  };
  decision = stdenvNoCC.mkDerivation {
    pname = "painted-wolf-code-decision-resources";
    version = pins.decision.release;
    dontUnpack = true;
    nativeBuildInputs = [ python3 ];
    installPhase = ''
      runHook preInstall
      mkdir heads
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (name: file: ''cp ${file} heads/${name}.safetensors'') headFiles)}
      python3 ${source}/scripts/stage-decide-heads.py \
        --manifest ${source}/lycaon/config/packs/painted-wolf/platform/host/decision-release.json \
        --source heads --destination "$out/decide/heads"
      model="$out/decide/models/${lib.replaceStrings [ "/" ] [ "--" ] pins.model.id}@${builtins.substring 0 12 pins.model.revision}"
      mkdir -p "$model"
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (path: file: ''install -Dm0644 ${file} "$model/${path}"'') modelFiles)}
      printf '%s\n' ${pins.model.revision} > "$model/.complete"
      runHook postInstall
    '';
  };
  scannerOriginal = stdenvNoCC.mkDerivation {
    pname = "painted-wolf-code-scanner-producer";
    version = pins.scannerVersion;
    dontUnpack = true;
    dontFixup = true;
    installPhase = ''
      mkdir -p "$out/artifact"
      cp ${scannerArchive} "$out/${pins.scannerSHA256}.tar.gz"
      tar -xzf "$out/${pins.scannerSHA256}.tar.gz" -C "$out/artifact"
    '';
  };
}
