# Installs the prebuilt release archive for macOS arm64 or Linux x86_64 (checked against
# its sha256). Other platforms have no archive, so Homebrew builds HEAD from source.
# `make release` builds the archives and .github/workflows/release.yml publishes them. To
# bump: set version and each url/sha256 pair from the release's SHA256SUMS.
class Xmustard < Formula
  desc "Governed runtime memory and grounding for coding agents (MCP server)"
  homepage "https://github.com/just-very-queer/xMustard"
  version "0.1.0"
  license "MIT"

  head do
    url "https://github.com/just-very-queer/xMustard.git", branch: "main"

    depends_on "go" => :build
    depends_on "rust" => :build
  end

  on_macos do
    on_arm do
      url "https://github.com/just-very-queer/xMustard/releases/download/v0.1.0/xmustard-v0.1.0-darwin-arm64.tar.gz"
      sha256 "68775ed049c3198e78633333dcb8db308b917b357d9d1bd281a619b45b7fc479"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/just-very-queer/xMustard/releases/download/v0.1.0/xmustard-v0.1.0-linux-x86_64.tar.gz"
      sha256 "a8a2cc383cab4f3689bd2b231e30bca22276e28d22c3bcbaca2ae4cb3bf11b8e"
    end
  end

  def install
    if build.head?
      # The Rust core does the semantic work; the Go binaries shell out to it.
      # cargo install puts both crate binaries in bin: xmustard-core and xmustard-relay.
      system "cargo", "install", *std_cargo_args(path: "rust-core")
      cd "api-go" do
        %w[xmustard-api xmustard-mcp xmustard-ops].each do |cmd|
          system "go", "build", *std_go_args(output: bin/cmd), "./cmd/#{cmd}"
        end
      end
    else
      bin.install %w[xmustard-api xmustard-core xmustard-mcp xmustard-ops xmustard-relay]
    end
  end

  def caveats
    <<~EOS
      The API and MCP server find the Rust core (xmustard-core) on PATH, which
      Homebrew puts in #{HOMEBREW_PREFIX}/bin. Override with XMUSTARD_CORE_BIN.

      Run the API:    xmustard-api               # listens on 127.0.0.1:8042
      MCP for agents: xmustard-mcp               # stdio; set XMUSTARD_API_BASE
      Lighter stdio:  xmustard-relay             # native relay to the API's /mcp endpoint;
                                                 # set XMUSTARD_WORKSPACE_ID (HTTP has no cwd)

      The Linux binaries need glibc 2.39 or newer.
      See the README for MCP client registration and the nine tools.
    EOS
  end

  test do
    # xmustard-core answers a CLI subcommand without a server.
    assert_match "usage", shell_output("#{bin}/xmustard-core 2>&1", 2)

    # xmustard-mcp lists its tools over stdio JSON-RPC (no API needed for tools/list).
    out = pipe_output(
      "#{bin}/xmustard-mcp",
      %Q({"jsonrpc":"2.0","id":1,"method":"tools/list"}\n),
    )
    assert_match "\"ground\"", out
    assert_match "\"remember\"", out
  end
end
