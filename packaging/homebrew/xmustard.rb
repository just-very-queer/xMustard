class Xmustard < Formula
  desc "Governed runtime memory and grounding for coding agents (MCP server)"
  homepage "https://github.com/just-very-queer/xMustard"
  license "MIT"
  head "https://github.com/just-very-queer/xMustard.git", branch: "main"

  # For a tagged release, fill in the tarball URL and checksum:
  #   url "https://github.com/just-very-queer/xMustard/archive/refs/tags/v0.1.0.tar.gz"
  #   sha256 "..."
  #   version "0.1.0"

  depends_on "go" => :build
  depends_on "rust" => :build

  def install
    # The Rust core does the semantic work; the Go binaries shell out to it.
    cd "rust-core" do
      system "cargo", "build", "--release", "--bin", "xmustard-core"
      bin.install "target/release/xmustard-core"
    end

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

      See the README for MCP client registration and the eight tools.
    EOS
  end

  test do
    # xmustard-core answers a CLI subcommand without a server.
    assert_match "usage", shell_output("#{bin}/xmustard-core 2>&1", 2)

    # xmustard-mcp lists its tools over stdio JSON-RPC (no API needed for tools/list).
    out = pipe_output(
      "#{bin}/xmustard-mcp",
      %({"jsonrpc":"2.0","id":1,"method":"tools/list"}\n),
    )
    assert_match "\"ground\"", out
    assert_match "\"remember\"", out
  end
end
