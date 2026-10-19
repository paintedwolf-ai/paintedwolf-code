{ pkgs, nixpkgs }:
let
  package = pkgs.runCommand "painted-wolf-module-selected" {} ''
    mkdir -p "$out/lib/painted-wolf-code"
    touch "$out/lib/painted-wolf-code/manifest.json"
  '';
  previous = pkgs.runCommand "painted-wolf-module-previous" {} ''mkdir -p "$out"'';
  runtime = pkgs.runCommand "painted-wolf-module-runtime" {} ''mkdir -p "$out"'';
  evaluate = settings: (nixpkgs.lib.nixosSystem {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [ (import ./nixos.nix { packageFor = _: package; }) {
      system.stateVersion = "26.05";
      services.painted-wolf-code = settings;
    } ];
  }).config;
  enabled = evaluate { enable = true; additionalTrustedPackages = [ previous ]; extraRuntimePackages = [ runtime ]; };
  disabled = evaluate { enable = false; };
  override = evaluate { enable = true; package = previous; polkit.enable = false; };
  service = enabled.systemd.services.paintedwolf-vault-identity;
  policy = builtins.elemAt (pkgs.lib.splitString " " service.serviceConfig.ExecStart) 2;
in
assert service.serviceConfig.DynamicUser == "yes";
assert service.serviceConfig.CapabilityBoundingSet == "CAP_SYS_PTRACE";
assert service.serviceConfig.AmbientCapabilities == "CAP_SYS_PTRACE";
assert service.serviceConfig.NoNewPrivileges == "yes";
assert service.serviceConfig.ProtectSystem == "strict";
assert service.serviceConfig.StateDirectory == "paintedwolf-vault";
assert service.serviceConfig.StateDirectoryMode == "0700";
assert enabled.systemd.sockets.paintedwolf-vault-identity.socketConfig.SocketMode == "0666";
assert enabled.security.polkit.enable;
assert builtins.elem "/share/polkit-1" enabled.environment.pathsToLink;
assert !(disabled.systemd.services ? paintedwolf-vault-identity);
assert !(disabled.systemd.sockets ? paintedwolf-vault-identity);
assert pkgs.lib.hasPrefix "${previous}/lib/painted-wolf-code/bin/pw-vault-identity " override.systemd.services.paintedwolf-vault-identity.serviceConfig.ExecStart;
assert service.restartTriggers != [];
pkgs.runCommand "painted-wolf-code-nixos-module-check" { nativeBuildInputs = [ pkgs.jq ]; } ''
  jq -e --arg package ${pkgs.lib.escapeShellArg (toString package)} \
    --arg previous ${pkgs.lib.escapeShellArg (toString previous)} \
    --arg runtime ${pkgs.lib.escapeShellArg (toString runtime)} '
    .format_version == 1 and (.generations | length) == 2 and
    .generations[0].install_root == ($package + "/lib/painted-wolf-code") and
    .generations[1].install_root == ($previous + "/lib/painted-wolf-code") and
    all(.generations[]; (.store_paths | index($runtime)) != null) and
    (.generations[0].store_paths | index($package)) != null and
    (.generations[0].store_paths | index($previous)) == null
  ' ${policy}
  touch "$out"
''
