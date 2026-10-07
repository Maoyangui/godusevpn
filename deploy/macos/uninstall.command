#!/bin/sh
# 佛跳墙 macOS 双击卸载。打包时改名为「卸载佛跳墙.command」。
# 做的就是文档里那两行:godusevpn uninstall(撤服务、撤禁直连闸、恢复网络设置)+ 删掉程序;前一步失败就不删程序。
# 设置与订阅留在 /Library/Application Support/godusevpn,重装后还在。

finish() {
  echo
  if [ -t 0 ]; then printf '按回车键关闭窗口…'; read -r _; fi
  exit "$1"
}

clear 2>/dev/null
echo "=============================="
echo "   卸载佛跳墙"
echo "=============================="
echo
if [ ! -e /usr/local/bin/godusevpn ] && [ ! -d /Applications/godusevpn.app ]; then
  echo "这台 Mac 上没有装佛跳墙,不用卸载。"
  finish 0
fi
echo "会撤掉后台服务和「禁直连」的防火墙规则、把网络设置恢复成连接前的样子,再删掉程序。"
echo "设置和订阅会留在 /Library/Application Support/godusevpn(想彻底清掉,卸载后把这个文件夹也删了)。"
echo
printf '确定卸载吗?输入 y 再按回车:'
read -r ans
case "$ans" in
  y|Y|yes|YES) ;;
  *) echo "已取消。"; finish 0 ;;
esac

if pgrep -f "/Applications/godusevpn.app/Contents/MacOS/godusevpn" >/dev/null 2>&1; then
  osascript -e 'quit app "godusevpn"' >/dev/null 2>&1 || true
fi
echo
echo "接下来要输入开机密码(输入时不显示字符,输完按回车)。"
# 撤服务失败就别删程序:「禁直连」的规则在系统里,程序删了就没人能撤它 —— 严格全局下会断网且没有工具恢复。
sudo -p "开机密码:" sh -c '
  if [ -x /usr/local/bin/godusevpn ]; then /usr/local/bin/godusevpn uninstall || exit 3; fi
  rm -rf /Applications/godusevpn.app /usr/local/bin/godusevpn
'
rc=$?
echo
case $rc in
  0) echo "✅ 已卸载。"; finish 0 ;;
  3)
    echo "❌ 撤后台服务 / 防火墙规则时出错,程序先留着没删(规则在系统里,程序删了就没人能撤它)。"
    echo "   再双击一次这个文件试试;还不行的话把这个窗口截图发给我们。"
    echo "   断网了的话,在终端里执行这一行可以先恢复网络:sudo pfctl -a com.apple/godusevpn -F all"
    finish 1 ;;
  *) echo "❌ 没有卸载完成(多半是密码不对或者不是管理员账户)。"; finish 1 ;;
esac
