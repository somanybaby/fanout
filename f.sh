#!/usr/bin/env bash
# fanout 管理菜单
set -uo pipefail

WORK_DIR=/var/lib/fanout
SERVICE=fanout
BIN=/usr/local/bin/fanout
REPO="${REPO:-byJoey/fanout}"

G='\033[0;32m'; R='\033[0;31m'; Y='\033[0;33m'; B='\033[0;36m'; D='\033[2m'; N='\033[0m'

need_root() {
  [[ $EUID -eq 0 ]] || { echo -e "${R}需要 root${N}"; exit 1; }
}

svc_state() {
  systemctl is-active --quiet "$SERVICE" && echo running || echo stopped
}

web_port() {
  grep -oE '\-web [0-9]+' /etc/systemd/system/${SERVICE}.service 2>/dev/null \
    | grep -oE '[0-9]+' | head -1 || echo 8899
}

public_ip() {
  curl -s --max-time 6 http://api.ipify.org 2>/dev/null || echo "<本机IP>"
}

pause() {
  echo
  read -rp "回车返回菜单..." _
}

show_info() {
  local state port bp pw ip
  state=$(svc_state); port=$(web_port)
  bp=$(cat "$WORK_DIR/basepath" 2>/dev/null || echo "-")
  pw=$(cat "$WORK_DIR/password" 2>/dev/null || echo "-")
  ip=$(public_ip)

  echo
  if [[ $state == running ]]; then
    echo -e "  状态      ${G}运行中${N}"
  else
    echo -e "  状态      ${R}已停止${N}"
  fi
  echo -e "  版本      $("$BIN" -version 2>/dev/null || echo '-')"
  echo -e "  开机自启  $(systemctl is-enabled "$SERVICE" 2>/dev/null || echo '-')"
  echo
  echo -e "  ${B}管理地址  http://${ip}:${port}/${bp}/${N}"
  echo -e "  ${B}访问口令  ${pw}${N}"
  echo

  local n
  n=$(ls -d /var/run/netns/fo* 2>/dev/null | wc -l | tr -d ' ')
  echo -e "  ${D}运行中的隧道: ${n}${N}"
}

list_tunnels() {
  local port bp pw ck
  port=$(web_port)
  bp=$(cat "$WORK_DIR/basepath" 2>/dev/null)
  pw=$(cat "$WORK_DIR/password" 2>/dev/null)
  ck=$(mktemp)

  curl -s --max-time 10 -c "$ck" -X POST -d "password=${pw}" \
    "http://127.0.0.1:${port}/${bp}/login" -o /dev/null
  echo
  curl -s --max-time 10 -b "$ck" "http://127.0.0.1:${port}/${bp}/api/tunnels" \
    > "$ck.json" 2>/dev/null
  rm -f "$ck"

  python3 - "$ck.json" <<'PYEOF'
import sys, json

try:
    with open(sys.argv[1]) as f:
        rows = json.load(f)
except Exception:
    print("  读取失败，服务可能没在运行")
    sys.exit()

if not isinstance(rows, list) or not rows:
    print("  还没有隧道，去网页里添加")
    sys.exit()

print("  端口      状态       出口 IP           节点")
for t in rows:
    port = str(t.get("port", "-"))
    status = t.get("status", "-")
    exit_ip = t.get("exit_ip") or "-"
    host = t.get("node", {}).get("hostname", "-")
    print("  %-10s%-11s%-18s%s" % (port, status, exit_ip, host))
PYEOF
  rm -f "$ck.json"
}

change_port() {
  local cur new
  cur=$(web_port)
  echo
  read -rp "  新端口 (当前 ${cur}): " new
  [[ -z $new ]] && { echo "  未修改"; return; }
  if ! [[ $new =~ ^[0-9]+$ ]] || (( new < 1 || new > 65535 )); then
    echo -e "  ${R}端口不合法${N}"; return
  fi
  if ss -tln 2>/dev/null | grep -q ":${new} "; then
    echo -e "  ${R}端口 ${new} 已被占用${N}"; return
  fi
  sed -i "s/-web ${cur}/-web ${new}/" /etc/systemd/system/${SERVICE}.service
  systemctl daemon-reload
  systemctl restart "$SERVICE"
  echo -e "  ${G}已改为 ${new} 并重启${N}"
}

reset_password() {
  local pw
  echo
  read -rp "  新口令 (留空则随机生成): " pw
  if [[ -z $pw ]]; then
    pw=$(head -c 9 /dev/urandom | od -An -tx1 | tr -d ' \n')
  fi
  umask 077
  echo "$pw" > "$WORK_DIR/password"
  systemctl restart "$SERVICE"
  echo -e "  ${G}新口令: ${pw}${N}"
}

reset_basepath() {
  local bp
  echo
  read -rp "  新访问路径 (留空则随机生成): " bp
  if [[ -z $bp ]]; then
    rm -f "$WORK_DIR/basepath"
    systemctl restart "$SERVICE"
    sleep 2
    bp=$(cat "$WORK_DIR/basepath" 2>/dev/null)
  else
    bp=${bp#/}; bp=${bp%/}
    umask 077
    echo "$bp" > "$WORK_DIR/basepath"
    systemctl restart "$SERVICE"
  fi
  echo -e "  ${G}新路径: /${bp}/${N}"
}

ipv6_state() {
  local a d
  a=$(sysctl -n net.ipv6.conf.all.disable_ipv6 2>/dev/null || echo 0)
  d=$(sysctl -n net.ipv6.conf.default.disable_ipv6 2>/dev/null || echo 0)
  [[ "$a" == 1 && "$d" == 1 ]] && echo disabled || echo enabled
}

toggle_ipv6() {
  local conf=/etc/sysctl.d/99-fanout-ipv6.conf
  echo
  if [[ $(ipv6_state) == disabled ]]; then
    read -rp "  当前已禁用 IPv6，要重新启用吗？[y/N]: " yes
    [[ ${yes,,} == y ]] || { echo "  已取消"; return; }
    rm -f "$conf"
    sysctl -qw net.ipv6.conf.all.disable_ipv6=0
    sysctl -qw net.ipv6.conf.default.disable_ipv6=0
    sysctl -qw net.ipv6.conf.lo.disable_ipv6=0
    echo -e "  ${G}已重新启用 IPv6${N}"
    return
  fi

  echo -e "  ${D}母机有全局 IPv6 时，没走隧道的流量可能从 IPv6 出去，暴露真实地址。${N}"
  read -rp "  确认禁用整机 IPv6？[y/N]: " yes
  [[ ${yes,,} == y ]] || { echo "  已取消"; return; }

  cat > "$conf" <<EOF
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
  sysctl -qw net.ipv6.conf.all.disable_ipv6=1
  sysctl -qw net.ipv6.conf.default.disable_ipv6=1
  sysctl -qw net.ipv6.conf.lo.disable_ipv6=1
  systemctl restart "$SERVICE" 2>/dev/null
  echo -e "  ${G}已禁用 IPv6（重启后依然生效）${N}"
}

show_links() {
  echo
  echo -e "  交流群  ${B}https://t.me/+ft-zI76oovgwNmRh${N}"
  echo -e "  油管    ${B}https://youtube.com/@joeyblog${N}"
  echo -e "  博客    ${B}https://joeyblog.net${N}"
  echo -e "  项目    ${B}https://github.com/byJoey/fanout${N}"
  echo
  echo -e "  ${D}用着有问题、或者想要什么功能，去群里说或提 issue。${N}"
}

do_update() {
  local arch goarch tmp
  arch=$(uname -m)
  case "$arch" in
    x86_64) goarch=amd64 ;;
    aarch64|arm64) goarch=arm64 ;;
    *) echo -e "  ${R}不支持的架构 ${arch}${N}"; return ;;
  esac

  echo -e "\n  当前 $("$BIN" -version 2>/dev/null || echo '-')"
  tmp=$(mktemp -d)
  echo "  正在下载最新版..."
  if ! curl -fsSL "https://github.com/${REPO}/releases/latest/download/fanout-linux-${goarch}.tar.gz" \
       -o "$tmp/f.tar.gz"; then
    echo -e "  ${R}下载失败${N}"; rm -rf "$tmp"; return
  fi
  tar xzf "$tmp/f.tar.gz" -C "$tmp"
  systemctl stop "$SERVICE"
  install -m 755 "$tmp/fanout" "$BIN"
  systemctl start "$SERVICE"
  rm -rf "$tmp"
  echo -e "  ${G}已更新到 $("$BIN" -version 2>/dev/null)${N}"
}

do_uninstall() {
  local yes
  echo
  read -rp "  确认卸载？隧道和配置都会删除 [y/N]: " yes
  [[ ${yes,,} == y ]] || { echo "  已取消"; return; }

  systemctl stop "$SERVICE" 2>/dev/null
  systemctl disable "$SERVICE" 2>/dev/null
  # 清掉残留的 netns 与 veth
  for ns in $(ip netns list 2>/dev/null | awk '{print $1}' | grep '^fo[0-9]'); do
    ip netns del "$ns" 2>/dev/null
  done
  for l in $(ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | grep '^fov[0-9]'); do
    ip link del "$l" 2>/dev/null
  done
  rm -f /etc/systemd/system/${SERVICE}.service "$BIN" /usr/local/bin/f
  rm -rf "$WORK_DIR"
  systemctl daemon-reload
  echo -e "  ${G}已卸载${N}"
  exit 0
}

menu() {
  while true; do
    clear
    echo -e "${B}  fanout${N}  ${D}VPN Gate 出口扇出网关${N}"
    show_info
    echo -e "${D}  ─────────────────────────────${N}"
    echo "   1) 启动          2) 停止"
    echo "   3) 重启          4) 查看日志"
    echo
    echo "   5) 隧道列表      6) 连接信息"
    echo
    echo "   7) 改端口        8) 改口令"
    echo "   9) 改访问路径   10) 开机自启开关"
    echo
    echo "  11) 更新         12) 卸载"
    echo "  13) 交流群 / 反馈"
    echo "   0) 退出"
    echo -e "${D}  ─────────────────────────────${N}"
    read -rp "  选择: " choice

    case "$choice" in
      1) systemctl start "$SERVICE"   && echo -e "\n  ${G}已启动${N}"; pause ;;
      2) systemctl stop "$SERVICE"    && echo -e "\n  ${Y}已停止${N}"; pause ;;
      3) systemctl restart "$SERVICE" && echo -e "\n  ${G}已重启${N}"; pause ;;
      4) echo; journalctl -u "$SERVICE" -n 40 --no-pager; pause ;;
      5) list_tunnels; pause ;;
      6) show_info; pause ;;
      7) change_port; pause ;;
      8) reset_password; pause ;;
      9) reset_basepath; pause ;;
      10)
        if systemctl is-enabled --quiet "$SERVICE"; then
          systemctl disable "$SERVICE" >/dev/null 2>&1
          echo -e "\n  ${Y}已关闭开机自启${N}"
        else
          systemctl enable "$SERVICE" >/dev/null 2>&1
          echo -e "\n  ${G}已开启开机自启${N}"
        fi
        pause ;;
      11) do_update; pause ;;
      13) show_links; pause ;;
      12) do_uninstall; pause ;;
      0) exit 0 ;;
      *) ;;
    esac
  done
}

need_root

# 带参数时当普通命令用，不进菜单
case "${1:-}" in
  start)    systemctl start "$SERVICE" ;;
  stop)     systemctl stop "$SERVICE" ;;
  restart)  systemctl restart "$SERVICE" ;;
  status)   systemctl status "$SERVICE" --no-pager ;;
  log)      journalctl -u "$SERVICE" -f ;;
  info)     show_info ;;
  list)     list_tunnels ;;
  update)   do_update ;;
  uninstall) do_uninstall ;;
  "")       menu ;;
  *)
    echo "用法: f [start|stop|restart|status|log|info|list|update|uninstall]"
    echo "不带参数进入交互菜单"
    ;;
esac
