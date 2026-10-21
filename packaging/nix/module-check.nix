{ pkgs, nixpkgs }:
let
  fixture = name: pkgs.runCommand name { nativeBuildInputs = [ pkgs.python3 ]; } ''
    mkdir -p "$out/lib/painted-wolf-code/bin"
    python - "$out" ${pkgs.lib.escapeShellArg pkgs.stdenv.hostPlatform.uname.processor} <<'PY'
    import hashlib, json, pathlib, struct, sys
    root = pathlib.Path(sys.argv[1]) / "lib/painted-wolf-code"
    engine = root / "bin/pw"
    data = b"\x7fELF\x02\x01\x01" + bytes(11) + struct.pack("<H", {"x86_64":62,"aarch64":183}[sys.argv[2]])
    engine.write_bytes(data)
    engine.chmod(0o755)
    (root / "manifest.json").write_text(json.dumps({"format_version":1,"package":"painted-wolf-code","architecture":sys.argv[2],"install_root":str(root),"executables":[{"path":str(engine),"sha256":hashlib.sha256(data).hexdigest(),"size":len(data)}]}))
    PY
  '';
  package = fixture "painted-wolf-module-selected";
  previous = fixture "painted-wolf-module-previous";
  runtime = pkgs.runCommand "painted-wolf-module-runtime" {} ''mkdir -p "$out"'';
  systemWith = apparmor: desktop: settings: nixpkgs.lib.nixosSystem {
    system = pkgs.stdenv.hostPlatform.system;
    modules = [ (import ./nixos.nix { packageFor = _: package; }) desktop {
      system.stateVersion = "26.05";
      security.apparmor.enable = apparmor;
      services.painted-wolf-code = settings;
    } ];
  };
  evaluateWith = apparmor: settings: (systemWith apparmor { } settings).config;
  evaluate = evaluateWith false;
  # An ordinary desktop configuration with no runtime packages named for the vault.
  desktopSystem = systemWith false ({ pkgs, ... }: {
    hardware.graphics.enable = true;
    programs.dconf.enable = true;
    i18n.inputMethod = { enable = true; type = "ibus"; };
    users.users.member = { isNormalUser = true; packages = [ pkgs.hello ]; };
  }) { enable = true; };
  desktop = desktopSystem.config;
  desktopPolicy = builtins.elemAt (pkgs.lib.splitString " "
    desktop.systemd.services.paintedwolf-vault-identity.serviceConfig.ExecStart) 2;
  ownerOnly = pkgs.runCommand "painted-wolf-module-unselected" {} ''mkdir -p "$out"'';
  enabled = evaluate { enable = true; additionalTrustedPackages = [ previous ]; extraRuntimePackages = [ runtime ]; };
  disabled = evaluate { enable = false; };
  override = evaluate { enable = true; package = previous; polkit.enable = false; };
  service = enabled.systemd.services.paintedwolf-vault-identity;
  policy = builtins.elemAt (pkgs.lib.splitString " " service.serviceConfig.ExecStart) 2;
  restricted = evaluateWith true { enable = true; additionalTrustedPackages = [ previous ]; };
  restrictedDisabled = evaluateWith true { enable = false; };
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
assert !(enabled.security.apparmor.policies ? paintedwolf-selected-engines);
assert !(restrictedDisabled.security.apparmor.policies ? paintedwolf-selected-engines);
assert restricted.security.apparmor.enable;
assert restricted.security.apparmor.policies.paintedwolf-selected-engines.state == "enforce";
pkgs.runCommand "painted-wolf-code-nixos-module-check" { nativeBuildInputs = [ pkgs.jq ]; } ''
  test "$(grep -c '^profile paintedwolf-engine-' ${restricted.security.apparmor.policies.paintedwolf-selected-engines.path})" = 2
  grep -F '"${package}/lib/painted-wolf-code/bin/pw"' ${restricted.security.apparmor.policies.paintedwolf-selected-engines.path}
  grep -F '"${previous}/lib/painted-wolf-code/bin/pw"' ${restricted.security.apparmor.policies.paintedwolf-selected-engines.path}
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
  jq -e --arg package ${pkgs.lib.escapeShellArg (toString package)} \
    --arg path ${pkgs.lib.escapeShellArg (toString desktop.system.path)} \
    --arg graphics ${pkgs.lib.escapeShellArg (toString desktop.hardware.graphics.package)} \
    --arg dconf ${pkgs.lib.escapeShellArg (toString desktopSystem.pkgs.dconf.lib)} \
    --arg input ${pkgs.lib.escapeShellArg (toString desktop.i18n.inputMethod.package)} \
    --arg member ${pkgs.lib.escapeShellArg (toString desktopSystem.pkgs.hello)} \
    --arg foreign ${pkgs.lib.escapeShellArg (toString ownerOnly)} '
    (.generations | length) == 1 and
    all([$package, $path, $graphics, $dconf, $input, $member][];
        . as $required | (.generations[0].store_paths | index($required)) != null) and
    (.generations[0].store_paths | index($foreign)) == null
  ' ${desktopPolicy}
  touch "$out"
''
