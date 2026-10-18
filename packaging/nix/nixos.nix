{ packageFor ? (_: null) }:
{ config, lib, pkgs, ... }:
let
  inherit (lib) mkEnableOption mkIf mkOption types;
  cfg = config.services.painted-wolf-code;
  selected = [ cfg.package ] ++ cfg.additionalTrustedPackages;
  closures = map (package: {
    inherit package;
    closure = pkgs.closureInfo { rootPaths = [ package ] ++ cfg.extraRuntimePackages; };
  }) selected;
  policy = pkgs.runCommand "painted-wolf-code-custody-policy.json" {
    nativeBuildInputs = [ pkgs.jq ];
  } ''
    mkdir generations
    ${lib.concatImapStringsSep "\n" (index: entry: ''
      jq -Rn --arg root ${lib.escapeShellArg "${entry.package}/lib/painted-wolf-code"} '
        {install_root: $root, manifest: ($root + "/manifest.json"),
         store_paths: [inputs | select(length > 0)]}
      ' < ${entry.closure}/store-paths > generations/${toString index}.json
    '') closures}
    jq -s --arg store ${lib.escapeShellArg builtins.storeDir} '
      {format_version: 1, store_directory: $store, generations: .}
    ' generations/*.json > "$out"
  '';
  # The package service is the common hardening contract for every Linux init.
  serviceContract = lib.foldl' (state: line:
    if lib.hasPrefix "[" line then state // { active = line == "[Service]"; }
    else if !state.active || line == "" || lib.hasPrefix "#" line then state
    else let setting = builtins.match "([^=]+)=(.*)" line;
    in if setting == null then throw "Unsupported Painted Wolf Code service directive"
       else state // { settings = state.settings // {
         ${builtins.elemAt setting 0} = builtins.elemAt setting 1;
       }; }
  ) { active = false; settings = {}; }
    (lib.splitString "\n" (builtins.readFile ../linux/systemd/paintedwolf-vault-identity.service));
in {
  options.services.painted-wolf-code = {
    enable = mkEnableOption "Painted Wolf Code desktop package and protected vault identity custody";
    package = mkOption {
      type = types.package;
      default = let package = packageFor pkgs.stdenv.hostPlatform.system;
        in if package == null then throw "Set services.painted-wolf-code.package to the selected package generation" else package;
      description = "Package generation installed and explicitly trusted by the vault service.";
    };
    additionalTrustedPackages = mkOption {
      type = types.listOf types.package;
      default = [];
      description = "Additional exact generations trusted during an intentional transition. Removing one restarts custody and revokes its connections.";
    };
    extraRuntimePackages = mkOption {
      type = types.listOf types.package;
      default = [];
      description = "Explicit runtime outputs, such as selected graphics drivers, whose executable mappings are allowed for each selected generation.";
    };
    polkit.enable = mkOption {
      type = types.bool;
      default = true;
      description = "Enable the system polkit service and install the package's authentication actions. This grants no passwordless authorization.";
    };
  };
  config = mkIf cfg.enable {
    assertions = [{
      assertion = builtins.length selected <= 8 && builtins.length (lib.unique (map toString selected)) == builtins.length selected;
      message = "Painted Wolf Code custody requires one to eight distinct, explicitly selected package generations.";
    }];
    environment.systemPackages = [ cfg.package ];
    environment.pathsToLink = [ "/share/polkit-1" ];
    security.polkit.enable = lib.mkIf cfg.polkit.enable true;
    systemd.sockets.paintedwolf-vault-identity = {
      description = "Painted Wolf Code vault identity socket";
      wantedBy = [ "sockets.target" ];
      socketConfig = {
        ListenStream = "/run/painted-wolf-code/vault-identity.sock";
        SocketMode = "0666";
        DirectoryMode = "0755";
        RemoveOnStop = true;
      };
    };
    systemd.services.paintedwolf-vault-identity = {
      description = "Painted Wolf Code vault identity service";
      requires = [ "paintedwolf-vault-identity.socket" ];
      restartTriggers = [ policy ];
      serviceConfig = serviceContract.settings // {
        ExecStart = "${cfg.package}/lib/painted-wolf-code/bin/pw-vault-identity --trust-policy ${policy} --require-dedicated-protection";
      };
    };
  };
}
