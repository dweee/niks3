{
  description = "Development environment for this project";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/6ff04cacda7e836e602bb6ecd84ca5487fbc6ffe";

    treefmt-nix.url = "github:numtide/treefmt-nix/a0a97e31a897a3537eea5c49df4f8adaa83b3b37";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    inputs@{ nixpkgs, ... }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];

      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      nixosModules = {
        niks3 = ./nix/nixosModules/niks3.nix;
        niks3-auto-upload = ./nix/nixosModules/niks3-auto-upload.nix;
        default = ./nix/nixosModules/niks3.nix;
      };

      darwinModules = {
        niks3-auto-upload = ./nix/darwinModules/niks3-auto-upload.nix;
      };

      dashboards.niks3 = ./deploy/helm/niks3/files/niks3.json;

      packages = forAllSystems (_: pkgs: import ./nix/packages { inherit pkgs; });

      checks = forAllSystems (
        system: pkgs:
        import ./nix/checks {
          inherit pkgs;
          selfPackages = inputs.self.packages.${system};
          selfDevShells = inputs.self.devShells.${system} or { };
          treefmtCheck =
            (import ./nix/formatter {
              inherit pkgs;
              inherit (inputs) treefmt-nix;
            }).check
              inputs.self;
        }
      );

      devShells = forAllSystems (
        system: pkgs:
        import ./nix/devshells {
          inherit pkgs;
          selfPackages = inputs.self.packages.${system};
        }
      );

      formatter = forAllSystems (
        _: pkgs:
        (import ./nix/formatter {
          inherit pkgs;
          inherit (inputs) treefmt-nix;
        }).wrapper
      );
    };
}
