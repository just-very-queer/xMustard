class Xmustard < Formula
  desc "Governed runtime memory and grounding for coding agents (MCP server)"
  homepage "https://github.com/just-very-queer/xMustard"
  url "https://github.com/just-very-queer/xMustard/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "e47990365fdd23c7e9e63a2005058c14e6d116934fee511ec1d467e275dac0d4"
  license "MIT"
  head "https://github.com/just-very-queer/xMustard.git", branch: "main"

  depends_on "go" => :build
  depends_on "rust" => :build

  def install
    # The Rust core does the semantic work; the Go binaries shell out to it.
    # Installs both crate binaries: xmustard-core and the stdio relay xmustard-relay.
    system "cargo", "install", *std_cargo_args(path: "rust-core")

    cd "api-go" do
      %w[xmustard-api xmustard-mcp xmustard-ops].each do |cmd|
        system "go", "build", "-o", bin/cmd, "./cmd/#{cmd}"
      end
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
