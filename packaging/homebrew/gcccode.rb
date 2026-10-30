class Gcccode < Formula
  desc "Schedule several Claude and ChatGPT subscriptions without dropping the chat"
  homepage "https://github.com/nearmetips/GCCcode"
  version "0.1.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/nearmetips/GCCcode/releases/download/v0.1.0/gcccode_0.1.0_darwin_arm64.tar.gz"
      sha256 "6df653b905f73a7ebf95993d5b649f08ee8abc204a709fb4f480600fce321e9e"
    end
    on_intel do
      url "https://github.com/nearmetips/GCCcode/releases/download/v0.1.0/gcccode_0.1.0_darwin_amd64.tar.gz"
      sha256 "662b8bca0bae5e0c453829382a6d4477ce9a5205a68efe9f4eeb68782226fc60"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/nearmetips/GCCcode/releases/download/v0.1.0/gcccode_0.1.0_linux_arm64.tar.gz"
      sha256 "859b3a82ebfec28c939af980958405d06f8cb3826d11aa1104e9bbd120fe8e2e"
    end
    on_intel do
      url "https://github.com/nearmetips/GCCcode/releases/download/v0.1.0/gcccode_0.1.0_linux_amd64.tar.gz"
      sha256 "b3baefc4534ea186af2f513c8ef13733b251af96da79da87838eef3b082ff407"
    end
  end

  def install
    bin.install "gcccode"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/gcccode version")
  end
end
