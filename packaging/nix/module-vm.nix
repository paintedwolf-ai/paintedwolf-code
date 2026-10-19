{ nixpkgs, system, package, desktop ? "gnome" }:
assert builtins.elem desktop [ "gnome" "plasma" ];
let
  evaluation = nixpkgs.lib.nixosSystem {
    inherit system;
    modules = [
      (nixpkgs + "/nixos/modules/virtualisation/qemu-vm.nix")
      (import ./nixos.nix { packageFor = _: package; })
      ({ lib, pkgs, ... }: {
        system.stateVersion = "26.05";
        networking.hostName = "paintedwolf-${desktop}-qualification";
        services.painted-wolf-code.enable = true;
        users.users.root.initialHashedPassword = "!";
        users.users.qualifier = {
          isNormalUser = true;
          initialHashedPassword = "!";
          extraGroups = [ "wheel" ];
        };
        services.displayManager.autoLogin = { enable = true; user = "qualifier"; };
        services.displayManager.gdm.enable = desktop == "gnome";
        services.desktopManager.gnome.enable = desktop == "gnome";
        services.gnome.gnome-initial-setup.enable = lib.mkIf (desktop == "gnome") false;
        services.displayManager.sddm.enable = desktop == "plasma";
        services.desktopManager.plasma6.enable = desktop == "plasma";
        services.openssh = {
          enable = true;
          settings = {
            PasswordAuthentication = false;
            KbdInteractiveAuthentication = false;
            PermitRootLogin = "prohibit-password";
            AuthorizedKeysFile = "/run/paintedwolf-qualification-keys/%u";
          };
        };
        boot.kernelModules = [ "qemu_fw_cfg" ];
        systemd.services.paintedwolf-qualification-key = {
          description = "Admit an operator-supplied public key for the disposable qualification guest";
          wantedBy = [ "multi-user.target" ];
          before = [ "sshd.service" ];
          after = [ "systemd-modules-load.service" ];
          serviceConfig = {
            Type = "oneshot";
            RuntimeDirectory = "paintedwolf-qualification-keys";
            RuntimeDirectoryMode = "0755";
            RemainAfterExit = true;
          };
          path = [ pkgs.coreutils pkgs.gnugrep pkgs.openssh ];
          script = ''
            set -euo pipefail
            umask 077
            key=/sys/firmware/qemu_fw_cfg/by_name/opt/paintedwolf/ssh-public-key/raw
            if ! test -r "$key"; then exit 0; fi
            candidate="$RUNTIME_DIRECTORY/candidate"
            trap 'rm -f "$candidate"' EXIT
            head -c 4097 "$key" > "$candidate"
            test "$(wc -c < "$candidate")" -le 4096
            test "$(grep -ac '^' "$candidate")" -eq 1
            LC_ALL=C grep -aEq '^ssh-ed25519 [A-Za-z0-9+/]+={0,2}( [ -~]*)?$' "$candidate"
            ssh-keygen -l -f "$candidate" > /dev/null
            install -m0644 "$candidate" "$RUNTIME_DIRECTORY/root"
            install -m0644 "$candidate" "$RUNTIME_DIRECTORY/qualifier"
          '';
        };
        environment.etc."paintedwolf-qualification.json".text = builtins.toJSON {
          format_version = 1;
          inherit desktop;
          package = toString package;
        };
        virtualisation = {
          memorySize = 4096;
          cores = 2;
          diskSize = 32768;
          diskImage = "./paintedwolf-nixos-${desktop}.qcow2";
          useBootLoader = true;
          mountHostNixStore = false;
          qemu.forceAccel = true;
        };
      })
    ];
  };
in evaluation.config.system.build.vm.overrideAttrs (previous: {
  passthru = (previous.passthru or {}) // {
    inherit desktop;
    system = evaluation.config.system.build.toplevel;
  };
})
