class Summond < Formula
  desc "Schedule and manage macOS background jobs without writing a single plist"
  homepage "https://github.com/joshgummersall/summond"
  version "v0.0.1"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-arm64"
      sha256 "406f027012535f817e23c733ab4c27564a7d6d735f0af7f99d6812b21365c93c"
    else
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-amd64"
      sha256 "8fbfee6a38ea4baf90df45abf7e69c5c1dcc70c3b1b5990bde1b550479b84f17"
    end
  end

  def install
    if Hardware::CPU.arm?
      bin.install "summond-darwin-arm64" => "summond"
    else
      bin.install "summond-darwin-amd64" => "summond"
    end
  end

  test do
    system "#{bin}/summond", "--version"
  end
end
