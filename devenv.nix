{
  pkgs,
  inputs,
  ...
}:

let
  gomod2nix = inputs.gomod2nix.packages.${pkgs.stdenv.hostPlatform.system}.default;
in
{
  packages = with pkgs; [
    sqlite
    templ
    tailwindcss_4
    gopls
    gomod2nix
  ];

  languages.go.enable = true;
  env.GOMODCACHE = "/home/nicolae/.cache/go-template/pkg/mod";

  services.postgres.enable = true;

  git-hooks.hooks.go-deps = {
    enable = true;
    name = "go deps (tidy + gomod2nix)";
    entry = "bash -c 'export PATH=${pkgs.go}/bin:${gomod2nix}/bin:$PATH; exec ${./scripts/check-go-deps.sh}'";
    files = "^(go\\.mod|go\\.sum|gomod2nix\\.toml)$";
    pass_filenames = false;
  };

  enterShell = ''
    echo "Devenv environment loaded!"
    go version
  '';
}
