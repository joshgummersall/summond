class Summond < Formula
  desc "Schedule and manage macOS background jobs without writing a single plist"
  homepage "https://github.com/joshgummersall/summond"
  version "v0.0.2"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-arm64"
      sha256 "15f013ef410b90ddccc7a65eb76d64a793c8536de55eae14e4bb2fc5162e8ff2"
    else
      url "https://github.com/joshgummersall/summond/releases/download/#{version}/summond-darwin-amd64"
      sha256 "e27a52ce53e44bb4200d1cc94867f79c9bb8772b21355932e3a02acc77e6319b"
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
