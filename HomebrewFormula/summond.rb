class Summond < Formula
  desc "Schedule and manage macOS background jobs without writing a single plist"
  homepage "https://github.com/joshgummersall/summond"
  version "v0.0.5"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-arm64"
      sha256 "ac25d05a2c068335387b097edf8a4f586ac98c90a9a288e378aa92a6cd488dd2"
    else
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-amd64"
      sha256 "43033588b2d2e2eb8866f8baa005109ddb7bb99a6dbce59f5ffc41de64fad160"
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
    system "#{bin}/summond", "version"
  end
end
