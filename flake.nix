{
  description = "beacon: one task at a time, on top of CalDAV tasks";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (
        pkgs:
        let
          # All binaries come from the same Go module, so they share the
          # source and the vendorHash.
          buildBeacon =
            {
              pname,
              cmd ? pname, # which cmd/<name> to build
              description,
              freebsd ? false, # cross-compile a FreeBSD amd64 binary
            }:
            pkgs.buildGoModule {
              inherit pname;
              version = "0.1.0";
              src = ./.;
              # Update this hash whenever go.mod or go.sum change:
              # set it to pkgs.lib.fakeHash, run `nix build`, copy the hash it prints.
              vendorHash = "sha256-rLbu1aA2+JcgRlqTeiLacCzSlEVzqZLH4fPBkqPNQMM=";
              subPackages = [ "cmd/${cmd}" ];
              # CGO_ENABLED=0 gives a static binary with no C dependencies.
              env.CGO_ENABLED = 0;
              # buildGoModule sets GOOS/GOARCH for the build machine itself,
              # so the cross target is set right before "go build" runs.
              preBuild = pkgs.lib.optionalString freebsd ''
                export GOOS=freebsd GOARCH=amd64
              '';
              # Go puts cross-compiled binaries in bin/<os>_<arch>/.
              postInstall = pkgs.lib.optionalString freebsd ''
                mv $out/bin/freebsd_amd64/* $out/bin/
                rmdir $out/bin/freebsd_amd64
              '';
              ldflags = [
                "-s"
                "-w"
              ];
              # Tests are run by the native packages; a FreeBSD test binary
              # cannot run here.
              doCheck = !freebsd;
              meta = {
                inherit description;
                mainProgram = cmd;
              };
            };
        in
        {
          t = buildBeacon {
            pname = "t";
            description = "beacon CLI: shows the current task";
          };
          beacon = buildBeacon {
            pname = "beacon";
            description = "beacon server: HTTP API with focus state on top of CalDAV";
          };
          beacon-freebsd-amd64 = buildBeacon {
            pname = "beacon-freebsd-amd64";
            cmd = "beacon";
            description = "beacon server, static binary for FreeBSD amd64";
            freebsd = true;
          };
          default = self.packages.${pkgs.stdenv.hostPlatform.system}.t;
        }
      );

      # `nix flake check` makes sure the FreeBSD package really is a static
      # FreeBSD binary (a wrong GOOS once produced a Linux one).
      checks = forAllSystems (pkgs: {
        freebsd-binary =
          pkgs.runCommand "check-freebsd-binary"
            {
              nativeBuildInputs = [ pkgs.file ];
              binary = "${self.packages.${pkgs.stdenv.hostPlatform.system}.beacon-freebsd-amd64}/bin/beacon";
            }
            ''
              info=$(file -L "$binary")
              echo "$info"
              for want in "ELF 64-bit" "x86-64" "FreeBSD" "statically linked"; do
                case "$info" in
                  *"$want"*) ;;
                  *) echo "missing: $want"; exit 1 ;;
                esac
              done
              touch $out
            '';
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            golangci-lint
          ];
        };
      });
    };
}
