#!/bin/sh
# ==============================================================================
# wopan-cli 一键安装脚本
# 支持系统: Linux (x86_64, aarch64/arm64), macOS (arm64)
# ==============================================================================
set -eu

REPO="cnzhwei/wopan-cli"
INSTALL_DIR="/usr/local/bin"

printf "\033[1;36m==> 正在检测系统与 CPU 架构...\033[0m\n"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64)
        ARCH_NAME="amd64"
        ;;
    aarch64|arm64)
        ARCH_NAME="arm64"
        ;;
    *)
        printf "\033[0;31m[!] 不支持的架构: %s\033[0m\n" "$ARCH" >&2
        exit 1
        ;;
esac

case "$OS" in
    linux)
        OS_NAME="linux"
        ;;
    darwin)
        OS_NAME="darwin"
        ;;
    *)
        printf "\033[0;31m[!] 不支持的操作系统: %s\033[0m\n" "$OS" >&2
        exit 1
        ;;
esac

TARGET_TAR="wopan-cli-${OS_NAME}-${ARCH_NAME}.tar.gz"

printf "    检测到系统: %s, 架构: %s\n" "$OS_NAME" "$ARCH_NAME"
printf "\033[1;36m==> 获取最新发布版本信息...\033[0m\n"

LATEST_RELEASE_JSON="$(curl -sL --connect-timeout 5 --max-time 15 "https://api.github.com/repos/${REPO}/releases/latest" || echo "{}")"
DOWNLOAD_URL="$(echo "$LATEST_RELEASE_JSON" | grep "browser_download_url" | grep "$TARGET_TAR" | cut -d '"' -f 4 | head -n1 || true)"

if [ -z "$DOWNLOAD_URL" ]; then
    DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${TARGET_TAR}"
fi

TMP_DIR="$(mktemp -d /tmp/wopan-install.XXXXXX)"
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

printf "    下载地址: %s\n" "$DOWNLOAD_URL"
printf "\033[1;36m==> 正在下载发布包...\033[0m\n"

curl -sSL --retry 2 --connect-timeout 10 --max-time 60 "$DOWNLOAD_URL" -o "${TMP_DIR}/${TARGET_TAR}"
tar -xzf "${TMP_DIR}/${TARGET_TAR}" -C "$TMP_DIR"

if [ ! -f "${TMP_DIR}/wopan-cli" ]; then
    printf "\033[0;31m[!] 解压失败，未找到可执行文件\033[0m\n" >&2
    exit 1
fi

chmod +x "${TMP_DIR}/wopan-cli"

printf "\033[1;36m==> 安装到 %s/wopan-cli ...\033[0m\n"

if [ -w "$INSTALL_DIR" ]; then
    mv "${TMP_DIR}/wopan-cli" "${INSTALL_DIR}/wopan-cli"
else
    if command -v sudo >/dev/null 2>&1; then
        sudo mv "${TMP_DIR}/wopan-cli" "${INSTALL_DIR}/wopan-cli"
    else
        printf "\033[0;33m[!] %s 目录不可写，尝试安装至 $HOME/.local/bin/ ...\033[0m\n" "$INSTALL_DIR"
        mkdir -p "$HOME/.local/bin"
        mv "${TMP_DIR}/wopan-cli" "$HOME/.local/bin/wopan-cli"
        INSTALL_DIR="$HOME/.local/bin"
    fi
fi

printf "\n\033[1;32m[✓] 安装成功！\033[0m\n"
"$INSTALL_DIR/wopan-cli" version
printf "输入 \033[1mwopan-cli help\033[0m 查看使用说明。\n\n"
