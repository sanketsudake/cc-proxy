{
  description = "Transparent logging proxy between Claude Code and the Anthropic API";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      # Keep in step with the release tag; goreleaser stamps the same string
      # into internal/version from {{ .Version }}.
      version = "0.1.0";

      systems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "aarch64-linux"
        "x86_64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        cc-proxy = pkgs.buildGoModule {
          pname = "cc-proxy";
          inherit version;
          src = self;

          vendorHash = "sha256-fqXpr9fV1jeT7503uAjb4jjNPlXfESFnXr7/Uc83d4o=";

          # Matches the goreleaser build: pure Go, no cgo.
          env.CGO_ENABLED = 0;

          ldflags = [
            "-s"
            "-w"
            "-X github.com/sanketsudake/cc-proxy/internal/version.Version=${version}"
            "-X github.com/sanketsudake/cc-proxy/internal/version.Commit=${self.shortRev or "dirty"}"
          ];

          meta = {
            description = "Transparent logging proxy between Claude Code and the Anthropic API";
            homepage = "https://github.com/sanketsudake/cc-proxy";
            license = pkgs.lib.licenses.mit;
            mainProgram = "cc-proxy";
          };
        };
        default = cc-proxy;
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
