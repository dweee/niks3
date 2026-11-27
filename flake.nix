{
  description = "Development environment for this project";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/0c08d75a1e6735054cfad1a121a4a7083dfe9034";

    treefmt-nix.url = "github:numtide/treefmt-nix/b7b56d3c8833016c650ca542b1230e6130299741";
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
