# Wiki 源文件

这一目录是 [GitHub Wiki](https://github.com/Maoyangui/godusevpn/wiki) 的源文件,放在仓库里跟代码一起走版本。
Wiki 那边是另一个 git 仓库(`godusevpn.wiki.git`),改完从这里同步过去:

```bash
git clone https://github.com/Maoyangui/godusevpn.wiki.git /tmp/wiki
cp docs/wiki/*.md /tmp/wiki/            # README.md 这一份不要拷
rm -f /tmp/wiki/README.md
cd /tmp/wiki && git add -A && git commit -m "同步 Wiki" && git push
```

> Wiki 的 git 仓库要在网页上建过第一页之后才存在,第一次同步前先去 Wiki 页点一下「Create the first page」随便存一页。

## 页面

| 文件 | 页面 |
|---|---|
| `Home.md` | 首页与导航 |
| `_Sidebar.md` / `_Footer.md` | 侧栏与页脚(GitHub 的约定文件名) |
| `安装 Windows/macOS/Linux/Android.md` | 四端安装 |
| `网关模式.md` | 软路由的网关模式与设备策略 |
| `日常使用.md` | 界面每一处是干嘛的 |
| `路由规则.md` | 默认规则与自定义规则组 |
| `关于 IPv6.md` | 五层怎么关、为什么要关到网卡、怎么验 |
| `设置项与文件位置.md` | 每个设置项与数据目录 |
| `命令行.md` | 全部子命令 |
| `排障.md` / `常见问题.md` | 出问题从哪查 |
| `从源码构建.md` | 构建标签、四端编法、测试与发版 |

站点(`site/`)是给第一次来的人看的介绍与上手,这里是**参考手册**,两边不要互相抄。
