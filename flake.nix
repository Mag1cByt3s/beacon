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
          # Both binaries come from the same Go module, so they share the
          # source and the vendorHash.
          buildBeacon =
            {
              pname,
              description,
            }:
            pkgs.buildGoModule {
              inherit pname;
              version = "0.1.0";
              src = ./.;
              # Update this hash whenever go.mod or go.sum change:
              # set it to pkgs.lib.fakeHash, run `nix build`, copy the hash it prints.
              vendorHash = "sha256-rLbu1aA2+JcgRlqTeiLacCzSlEVzqZLH4fPBkqPNQMM=";
              subPackages = [ "cmd/${pname}" ];
              env.CGO_ENABLED = 0;
              ldflags = [
                "-s"
                "-w"
              ];
              meta = {
                inherit description;
                mainProgram = pname;
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
          default = self.packages.${pkgs.stdenv.hostPlatform.system}.t;
        }
      );

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
