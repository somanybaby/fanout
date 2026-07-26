#!/usr/bin/env bash
# fanout 安装脚本：编译、装 systemd 服务、开机自启。
set -euo pipefail

WEB_PORT="${WEB_PORT:-8899}"
WORK_DIR="${WORK_DIR:-/var/lib/fanout}"
BIN=/usr/local/bin/fanout

if [[ $EUID -ne 0 ]]; then
  echo "需要 root 权限（要创建 netns 和改 iptables）" >&2
  exit 1
fi

echo "[1/5] 检查依赖"
need_pkg=()
command -v openvpn >/dev/null || need_pkg+=(openvpn)
command -v curl    >/dev/null || need_pkg+=(curl)
command -v ip      >/dev/null || need_pkg+=(iproute2)
command -v iptables >/dev/null || need_pkg+=(iptables)
if [[ ${#need_pkg[@]} -gt 0 ]]; then
  echo "      安装: ${need_pkg[*]}"
  if command -v apt-get >/dev/null; then
    apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${need_pkg[@]}"
  elif command -v dnf >/dev/null; then
    dnf install -y -q "${need_pkg[@]}"
  else
    echo "      不认识的包管理器，请手动安装: ${need_pkg[*]}" >&2
    exit 1
  fi
fi

echo "[2/5] 获取程序"
REPO="${REPO:-byJoey/fanout}"
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "      不支持的架构: $ARCH" >&2; exit 1 ;;
esac

if [[ -f main.go ]] && command -v go >/dev/null; then
  echo "      从源码编译"
  go build -trimpath -ldflags "-s -w" -o "$BIN" .
else
  echo "      下载预编译版本 (${GOARCH})"
  TMP=$(mktemp -d)
  URL="https://github.com/${REPO}/releases/latest/download/fanout-linux-${GOARCH}.tar.gz"
  if ! curl -fsSL "$URL" -o "$TMP/f.tar.gz"; then
    echo "      下载失败: $URL" >&2
    echo "      也可以 clone 仓库后在源码目录运行本脚本" >&2
    exit 1
  fi
  tar xzf "$TMP/f.tar.gz" -C "$TMP"
  install -m 755 "$TMP/fanout" "$BIN"
  [[ -f fanout.service ]] || cp "$TMP/fanout.service" .
  rm -rf "$TMP"
fi

echo "[3/5] 放行转发"
sysctl -qw net.ipv4.ip_forward=1
grep -q '^net.ipv4.ip_forward=1' /etc/sysctl.conf 2>/dev/null \
  || echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.conf
# FORWARD 链常有兜底 REJECT，fanout 用的网段要插到最前面
if ! iptables -C FORWARD -s 10.99.0.0/16 -j ACCEPT 2>/dev/null; then
  iptables -I FORWARD 1 -s 10.99.0.0/16 -j ACCEPT
fi
if ! iptables -C FORWARD -d 10.99.0.0/16 -j ACCEPT 2>/dev/null; then
  iptables -I FORWARD 1 -d 10.99.0.0/16 -j ACCEPT
fi
command -v netfilter-persistent >/dev/null && netfilter-persistent save >/dev/null 2>&1 || true

echo "[4/5] 安装服务"
mkdir -p "$WORK_DIR"
chmod 700 "$WORK_DIR"
sed "s#-web 8899#-web ${WEB_PORT}#; s#-dir /var/lib/fanout#-dir ${WORK_DIR}#" fanout.service \
  > /etc/systemd/system/fanout.service
systemctl daemon-reload
systemctl enable --now fanout

echo "[5/5] 就绪"
sleep 3
systemctl is-active --quiet fanout && echo "      服务运行中" || {
  echo "      服务启动失败，看 journalctl -u fanout -n 30" >&2
  exit 1
}

# 口令与访问路径由 fanout 首次启动时生成，等它写出来
for _ in $(seq 1 10); do
  [[ -s "${WORK_DIR}/password" && -s "${WORK_DIR}/basepath" ]] && break
  sleep 1
done

IP=$(curl -s --max-time 8 http://api.ipify.org || echo "<本机IP>")
BP=$(cat "${WORK_DIR}/basepath" 2>/dev/null || true)
echo
echo "  管理界面  http://${IP}:${WEB_PORT}/${BP}/"
echo "  访问口令  $(cat "${WORK_DIR}/password" 2>/dev/null || echo "见 ${WORK_DIR}/password")"
echo
echo "  路径和口令都是随机生成的，也可以随时查看："
echo "    cat ${WORK_DIR}/basepath"
echo "    cat ${WORK_DIR}/password"
echo
