{
  pkgs,
  # lib,
  # config,
  # inputs,
  ...
}:
{
  packages = with pkgs; [
    sqlite
    templ
    tailwindcss_4
    gopls
  ];

  languages.go.enable = true;
  env.GOMODCACHE = "/home/nicolae/.cache/go-template/pkg/mod";

  services.postgres.enable = true;

  enterShell = ''
    echo "Devenv environment loaded!"
    go version
  '';
}
