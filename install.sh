#!/bin/sh
# Install gcccode. Checksums always come from the release, never from a mirror.
#   curl -fsSL https://raw.githubusercontent.com/nearmetips/DailiCode/main/install.sh | sh
#   ... | sh -s -- --proxy http://127.0.0.1:7890 --mirror https://mirror.example/github.com
set -eu

repo="nearmetips/DailiCode"
version="${GCCCODE_VERSION:-}"
mirror=""
proxy=""

while [ $# -gt 0 ]; do
  case "$1" in
    --mirror) mirror="${2%/}/"; shift 2 ;;
    --proxy) proxy="$2"; shift 2 ;;
    *) echo "未知参数：$1" >&2; exit 1 ;;
  esac
done

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "不支持的架构：$arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin|linux) ;;
  *) echo "不支持的系统：$os。Windows 请用 Scoop 或发布页的 zip。" >&2; exit 1 ;;
esac

curl_base() {
  if [ -n "$proxy" ]; then
    curl -fsSL --proxy "$proxy" "$@"
  else
    curl -fsSL "$@"
  fi
}

if [ -z "$version" ]; then
  version=$(curl_base "https://api.github.com/repos/${repo}/releases/latest" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)
fi
if [ -z "$version" ]; then
  echo "没有拿到版本号。到 https://github.com/${repo}/releases 下载。" >&2
  exit 1
fi

name="gcccode_${version}_${os}_${arch}.tar.gz"
base="https://github.com/${repo}/releases/download/v${version}"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

curl_base "${base}/checksums.txt" -o "$tmpdir/checksums.txt"
curl_base "${mirror}${base}/${name}" -o "$tmpdir/${name}"

expected=$(grep " ${name}$" "$tmpdir/checksums.txt" | awk '{print $1}')
if [ -z "$expected" ]; then
  echo "校验文件里没有 ${name}" >&2
  exit 1
fi
if command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmpdir/${name}" | awk '{print $1}')
else
  got=$(sha256sum "$tmpdir/${name}" | awk '{print $1}')
fi
if [ "$got" != "$expected" ]; then
  echo "校验失败" >&2
  exit 1
fi

tar -C "$tmpdir" -xzf "$tmpdir/${name}"
dest="${HOME}/.local/bin"
mkdir -p "$dest"
install -m 0755 "$tmpdir/gcccode" "$dest/gcccode"
echo "已安装 ${dest}/gcccode"
case ":$PATH:" in
  *":${dest}:"*) ;;
  *) echo "把 ${dest} 加进 PATH 后再运行 gcccode。" ;;
esac
