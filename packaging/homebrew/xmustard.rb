# Every platform installs v0.1.0 checked against a sha256. macOS arm64 takes the prebuilt
# release archive. The other platforms build the tagged source tarball: macOS Intel and
# Linux arm64 have no archive, and v0.1.0's Linux x86_64 archive needs glibc 2.39, which
# Ubuntu 22.04, Debian 12 and RHEL 9 lack. `make release` builds the archives and
# .github/workflows/release.yml publishes them (its Linux leg now builds on Ubuntu 22.04,
# glibc 2.35). To bump: set version, the source tarball's sha256, and each archive
# url/sha256 pair from the release's SHA256SUMS.
class Xmustard < Formula
  desc "Governed runtime memory and grounding for coding agents (MCP server)"
  homepage "https://github.com/just-very-queer/xMustard"
  license "MIT"

  stable do
    version "0.1.0"

    on_macos do
      on_arm do
        url "https://github.com/just-very-queer/xMustard/releases/download/v0.1.0/xmustard-v0.1.0-darwin-arm64.tar.gz"
        sha256 "68775ed049c3198e78633333dcb8db308b917b357d9d1bd281a619b45b7fc479"
      end
      on_intel do
        url "https://github.com/just-very-queer/xMustard/archive/refs/tags/v0.1.0.tar.gz"
        sha256 "e47990365fdd23c7e9e63a2005058c14e6d116934fee511ec1d467e275dac0d4"

        depends_on "go" => :build
        depends_on "rust" => :build
      end
    end

    on_linux do
      url "https://github.com/just-very-queer/xMustard/archive/refs/tags/v0.1.0.tar.gz"
      sha256 "e47990365fdd23c7e9e63a2005058c14e6d116934fee511ec1d467e275dac0d4"

      depends_on "go" => :build
      depends_on "rust" => :build
    end
  end

  head do
    url "https://github.com/just-very-queer/xMustard.git", branch: "main"

    depends_on "go" => :build
    depends_on "rust" => :build
  end

  def install
    # A release archive holds the five binaries. A source tree (the tagged tarball or HEAD)
    # builds them: the Rust core does the semantic work and the Go binaries shell out to it.
    unless File.exist?("rust-core/Cargo.toml")
      bin.install %w[xmustard-api xmustard-core xmustard-mcp xmustard-ops xmustard-relay]
      return
    end

    # cargo install puts both crate binaries in bin: xmustard-core and xmustard-relay.
    system "cargo", "install", *std_cargo_args(path: "rust-core")
    cd "api-go" do
      %w[xmustard-api xmustard-mcp xmustard-ops].each do |cmd|
        system "go", "build", *std_go_args(output: bin/cmd), "./cmd/#{cmd}"
      end
    end
  end

  def caveats
    <<~EOS
      The API and MCP server find the Rust core (xmustard-core) on PATH, which
      Homebrew puts in #{HOMEBREW_PREFIX}/bin. Override with XMUSTARD_CORE_BIN.

      Set an absolute data directory before starting the API or xmustard-ops:
        export XMUSTARD_DATA_DIR="$HOME/.local/share/xmustard"
      The default, ../backend/data, is relative to the working directory and only
      fits a source checkout's api-go directory.

      Run the API:    xmustard-api               # listens on 127.0.0.1:8042
      MCP for agents: xmustard-mcp               # stdio; set XMUSTARD_API_BASE
      Lighter stdio:  xmustard-relay             # native relay to the API's /mcp endpoint;
                                                 # set XMUSTARD_WORKSPACE_ID (HTTP has no cwd)

      The prebuilt macOS arm64 binaries are built without cgo, so the platform
      profile's PTY terminals are unavailable in them. The default core profile
      is unaffected, and source builds keep the terminals.
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
