class Gcccode < Formula
  desc "Schedule several Claude and ChatGPT subscriptions without dropping the chat"
  homepage "https://github.com/nearmetips/DailiCode"
  version "0.1.0"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/nearmetips/DailiCode/releases/download/v0.1.0/gcccode_0.1.0_darwin_arm64.tar.gz"
      sha256 "REPLACE_AT_RELEASE"
    end
    on_intel do
      url "https://github.com/nearmetips/DailiCode/releases/download/v0.1.0/gcccode_0.1.0_darwin_amd64.tar.gz"
      sha256 "REPLACE_AT_RELEASE"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/nearmetips/DailiCode/releases/download/v0.1.0/gcccode_0.1.0_linux_arm64.tar.gz"
      sha256 "REPLACE_AT_RELEASE"
    end
    on_intel do
      url "https://github.com/nearmetips/DailiCode/releases/download/v0.1.0/gcccode_0.1.0_linux_amd64.tar.gz"
      sha256 "REPLACE_AT_RELEASE"
    end
  end

  def install
    bin.install "gcccode"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/gcccode version")
  end
end
