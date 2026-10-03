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
      packages = forAllSystems (pkgs: {
        t = pkgs.buildGoModule {
          pname = "t";
          version = "0.1.0";
          src = ./.;
          # Update this hash whenever go.mod or go.sum change:
          # set it to pkgs.lib.fakeHash, run `nix build`, copy the hash it prints.
          vendorHash = "sha256-8dYZRrXI+tNvQwMGFGG20h7Bjt/YtPQoLlRTtBiFUkM=";
          subPackages = [ "cmd/t" ];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
          ];
          meta = {
            description = "beacon CLI: shows the next task from CalDAV";
            mainProgram = "t";
          };
        };
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.t;
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
