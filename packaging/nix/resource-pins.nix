{ lib, source, system }:
let
  fingerprints = {
    "lycaon/config/gitengine/pin.yaml" = "c010337a856abbca84ea89afb86ab88315a1c98b6d02d0003fd5d351b7e3f9d4";
    "lycaon/config/runtime/ocrengine/pin.yaml" = "c662e51f2b11eb9cac8027be44ab530f9dc0b8d7418dea2db3abe9e21e202592";
    "lycaon/internal/browserengine/chrome_pin.go" = "813f9b921a0f99911926d9dfc8b31cd99c1011bc66dc18e32c46cb95de06a9bf";
    "lycaon/internal/decide/bialy/models.go" = "e1d59b931389b1b8edd1dd54a497eb9fde89476ca62742e8087d7a9e1d0d59b9";
    "lycaon/config/runtime/scanners/bundled-manifest.yaml" = "df0565f03d60c4aaffaef3f8647da63293fe683c7cc1792d7e1f5ad71c6e19b9";
  };
  platforms = {
    x86_64-linux = {
      platform = "linux-amd64";
      target = "x86_64-unknown-linux-gnu";
      chromePlatform = "linux64";
      browserSHA256 = "636aa5c79f2693632e9921b8bbb050038ba11672e02346c06c20f991aed096f9";
      gitSHA256 = "58ce0d7b5bbbf869f9047442ddd6d76d279ee35eb95fe1e658c956f645cf05b3";
      ocrSHA256 = "7104c62a1a43d72840c522433bbf60bed0618f223480b0628603f439c5444f06";
      scannerSHA256 = "3f475535a937edae0ad2429f3720af509c45365a7858925f04f6e92656bd5d61";
    };
    aarch64-linux = {
      platform = "linux-arm64";
      target = "aarch64-unknown-linux-gnu";
      chromePlatform = "linux-arm64";
      browserSHA256 = "0ed0e47d9e9f639197f508d62ada09e5c6b4c4c60edab3160a9312a733091df6";
      gitSHA256 = "c6afbebef764cee6f429ebf8902d465de40f12a7bcb40b95c6081a1f7221b6d6";
      ocrSHA256 = "be02c0c80ff58b2f7ffe8167da483fa4603ea197ce57667ecb9204641c6bcbd4";
      scannerSHA256 = "c10664d83045aecded510387fff078ceb53d3ff2f56f53f24e42ba3a74080d2a";
    };
  };
in
assert lib.assertMsg (lib.all (path:
  builtins.hashFile "sha256" (source + "/${path}") == fingerprints.${path}
) (builtins.attrNames fingerprints))
  "Review and update the Nix resource pins together with the runtime catalogs";
platforms.${system} // {
  browserVersion = "154.0.8037.92";
  gitVersion = "2.53.0";
  gitBuild = "paintedwolf.1";
  lfsVersion = "3.7.1";
  ocrVersion = "5.5.3+paintedwolf.1";
  scannerVersion = "1.30.0+paintedwolf.40";
  model = {
    id = "convaiinnovations/laya-multilingual";
    revision = "e4e9ddf21a7b1903b7acffd8814ad4307bf63a67";
    files = {
      "rl_agent_config.json" = "25061739243b617ad88d1219ba6f8a9c86c5881ca28df024fa2d9b3b2fcc30c6";
      "model.safetensors" = "9d628fd971b700382ac6f65920a86f149777b2e748e0c955fb3b19695aa8f204";
      "encoder/config.json" = "83f6916d13ef0f556ac461f28308dc2bffa7ebeadee8ec9e2db5812020ea5bb4";
      "tokenizer/tokenizer.json" = "609d8f4c067cd3950f88594c5a802616cea245823836ef5848ee4fc40aab5b6f";
      "tokenizer/tokenizer_config.json" = "424b69444bf7b5809dc2cd2e36d0bd71b8055124dd24274d6db3c655d38205e7";
    };
  };
  decision = lib.importJSON (source + "/lycaon/config/packs/painted-wolf/platform/host/decision-release.json");
}
