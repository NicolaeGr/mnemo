{
  description = "mnemo - a self-hosted contact and book manager";

  nixConfig = {
    extra-substituters = [ "https://nicolaegr.cachix.org" ];
    extra-trusted-public-keys = [
      "nicolaegr.cachix.org-1:wHSo9o0JcmUWG7+/tfFjFErV+3ZcToIYmtcDyTv1SDc="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    gomod2nix = {
      url = "github:nix-community/gomod2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      gomod2nix,
    }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];

      forEachSystem = f: nixpkgs.lib.genAttrs systems (system: f system);
    in
    {
      packages = forEachSystem (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          inherit (gomod2nix.legacyPackages.${system}) buildGoApplication;
        in
        {
          default = buildGoApplication {
            pname = "mnemo";
            version = "0.1.0";
            pwd = ./.;

            src = pkgs.lib.cleanSourceWith {
              src = self;
              filter =
                path: type:
                pkgs.lib.cleanSourceFilter path type
                && !(builtins.elem (baseNameOf path) [
                  ".devenv"
                  ".direnv"
                  "result"
                ]);
            };

            modules = ./gomod2nix.toml;

            postInstall = ''
              mkdir -p $out/share/mnemo
              cp -r ${./assets} $out/share/mnemo/assets
            '';

            nativeCheckInputs = [ pkgs.postgresql ];

            preCheck = ''
              export PGDATA="$TMPDIR/pgdata"
              export PGHOST="$TMPDIR/pgsocket"
              export PGUSER=mnemo
              export PGDATABASE=postgres
              mkdir -p "$PGHOST"
              initdb --no-locale --encoding=UTF8 --auth=trust -U "$PGUSER" >/dev/null
              pg_ctl start -D "$PGDATA" -w -o \
                "-k $PGHOST -c listen_addresses= -c fsync=off -c dynamic_shared_memory_type=mmap"
            '';

            postCheck = ''
              pg_ctl stop -D "$PGDATA" -m immediate
            '';

            meta = {
              description = "A self-hosted contact and book manager";
              homepage = "https://github.com/nicolaegr/mnemo";
              license = pkgs.lib.licenses.gpl3Only;
              mainProgram = "server";
              platforms = pkgs.lib.platforms.linux;
            };
          };
        }
      );

      apps = forEachSystem (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/server";
          meta.description = "Run the mnemo server";
        };
      });

      devShells = forEachSystem (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              gotools
              templ
              postgresql
              gomod2nix.legacyPackages.${system}.gomod2nix
            ];

            enterShell = ''
              go version
            '';
          };
        }
      );

      formatter = forEachSystem (system: nixpkgs.legacyPackages.${system}.nixfmt);
    };
}
