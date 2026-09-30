# Golang 远程桌面协议（RDP）客户端

grdp 是一个**纯 Go** 实现的微软远程桌面协议客户端。

Fork 自 [tomatome/grdp](https://github.com/tomatome/grdp)，后者 fork 自
[icodeface/grdp](https://github.com/icodeface/grdp)。

> 英文版说明见 [README.md](README.md)。真机验证的**方法**与**发现过程**记录在
> [docs/windows-verification.md](docs/windows-verification.md)，那是本仓库最值得读的一份文档。

## 状态

连接、认证、画面渲染、输入、剪贴板均已实现，并且**已在真实服务器上验证** ——
既走传统位图路径，也走较新的图形通道。

已对 **xrdp 0.9.24** 和一台**真实 Windows 10** 主机验证：

* [x] 连接、MCS、能力交换
* [x] TLS 与 NLA（CredSSP + NTLMv2，含 **版本 6** 的公钥绑定）
* [x] **Standard RDP Security**（无 TLS 的老路径）—— 已对本地 xrdp 实测通过；
      `scripts/dev-rdp.sh standard` 可把 xrdp 配置成要求它。它在同一目标上的输出与 TLS 路径
      **逐像素相同**，这比"能连上"强得多
* [x] 许可（Licensing）交换
* [x] 位图更新、RLE、24/32bpp
* [x] 光标位置，以及把光标形状**解码成可绘制的光标** —— AND/XOR 掩码已应用；
      能力集里宣告的 **20 个缓存槽真的实现了**，所以 CACHED 引用能取回形状而不是让光标消失；
      系统光标会如实标记；网关可从 `OnCursor` 拿到带 alpha 的 RGBA。
      已在 xrdp 上验证：它发的是双色光标，取回的热点值是真实光标该有的
      （箭头 `0,0`、工字 `4,8`），而不是垃圾值
* [x] 键盘与鼠标输入，含修饰键
* [x] 剪贴板文本**双向**
* [x] NSCodec 与 RemoteFX (RFX) 位图编解码器，**与 libfreerdp 的解码器逐字节比对**
* [x] ZGFX（EGFX 通道承载消息所用的批量压缩），同样**与 libfreerdp 逐字节比对**，
      并额外用**真实 Windows 抓包**核对
* [x] Surface Commands、`drdynvc`、RDPGFX 命令解析
* [x] **EGFX (RDPGFX) 渲染** —— 在真实 Windows 上验证：桌面、图标、文字全部经由
      图形通道送达，**完全不依赖位图更新**。默认关闭，见下文。
* [x] **绘图订单（Drawing Orders）** —— 位图缓存与 MEMBLT，按订单自带的 bounds 裁剪；
      另有 PATBLT（含画刷）、多边形、折线、椭圆、多矩形订单、TEXT2 背后的字形缓存，
      以及 LineTo、SaveBitmap、FastIndex。
      已在真实 Windows 上验证：整个锁屏界面经由缓存送达，**渐变与文字都正确**。与位图路径**差异仅剩任务栏时钟**（而同一路径跑两次的差异也在那里）。
      默认关闭，见下文。


* [x] **VNC（RFB）** —— 已对**真实 TigerVNC 服务端**验证：能收到并解析帧，
      且**剪贴板双向可用**。`scripts/vnc-dev.sh` 无需 root 即可拉取 TigerVNC 并跑实机测试。
      目前支持 Raw、CopyRect 与 Hextile 编码，`RequestClipboardText` 未实现（有测试记录这一点）。
      实机测试中看到 TigerVNC 对普通矩形选 **Hextile**、对真实滚动选 **CopyRect**，
      并校验复制出来的像素正是源位置当时的内容。

未实现 / 未完成：

* [ ] **RemoteFX Progressive**（codec id `0x0009` / `0x000D`）—— 需要独立的算术解码器。
      `THINCLIENT` 能力标志会让服务端不要用它；万一服务端仍用了，派发时的错误信息会点名。
* [ ] **交错 / 带状 RLE 位图** —— 只有位图缓存 **rev3** 会用到。实测：即使向 Windows
      广告 rev3，它**仍然只发 rev2**，而 rev2 用的解码器与位图更新相同。所以这是
      「没有实测路径能到达」，不是「跳过了」。
* [ ] **H.264 的解码** —— **分帧已实现**，并提供了 `AVCDecoder` 接口供调用方注入解码器；
      只有在设置了它时才广告这两个编解码器。纯 Go 写 H.264 解码器超出范围，
      而引入 cgo 会改变这个库的本质。没有解码器时**什么都不广告**，协商行为不变。
* [ ] **窗口化 surface 的摆放** —— `MapSurfaceToScaledOutput` 已实现（会重采样到服务端
      要求的尺寸），但两个窗口变体只**记录并对外暴露**、不实际应用，因为没有"窗口"可摆放。

## 什么被验证过、怎么验证的

这张表画出的区别，正是本仓库的意义所在。上面每一条主张都落在第一列或第二列，
而**凡在第一列的，都是用"不是写测试的那一方"验证过的**。

| | 依据 |
| --- | --- |
| 连接、TLS、NLA、许可、输入、剪贴板、位图更新 | 对 xrdp 0.9.24 与真实 Windows 10 运行过 |
| Standard RDP Security | 对要求它的本地 xrdp 运行过，且与同目标 TLS 路径**逐像素相同** |
| EGFX 渲染 | 整个 Windows 桌面经图形通道送达，并与位图路径对比 |
| 绘图订单 | 整个 Windows 锁屏经位图缓存送达，两条路径差异仅剩时钟 |
| VNC | 真实 TigerVNC：帧能收到，剪贴板双向可用 |
| NSCodec、RemoteFX、ZGFX | 与 libfreerdp **逐字节**比对（输入由其编码器产生，期望输出由其解码器给出）|
| 健壮性 | 模糊测试（抓到 2 个死循环、3 个 panic；语料已留存）|
| 形状/字形/九宫格/多矩形订单 | **仅**依据 FreeRDP 解析器 + 单测：没有可达服务端会发它们 |
| AVC420 / AVC444 分帧 | **仅**依据 FreeRDP 解析器 + 单测：解码需要本库不自带的解码器 |

## 作为库使用

```go
s := client.NewSetting()
s.Width, s.Height = 1024, 768
s.Protocol = "nla"           // "tls" 或 "nla"

c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)
c.OnBitmap(func(bs []client.Bitmap) { /* 绘制 */ })
c.OnClipboardText(func(text string) { /* 粘贴 */ })
if err := c.Login(); err != nil {
    log.Fatal(err)
}
c.KeyDown(0x1c, "")
c.KeyUp(0x1c, "")
```

* `Setting.EnableClipboard` —— 打开剪贴板通道。
* `Setting.EnableEGFX` —— 启用图形通道。**默认关闭**，因为服务端一旦选用它就会
  **停止发送位图更新**：此时若遇到不支持的编解码器，结果是**黑屏**而不是画质变差。
  用 `OnSurfaceFrame` 接收 surface，用 `Surface.Origin` 摆放。
* `Setting.EnableOrders` —— 广告 MEMBLT 并渲染随后的绘图订单，画面进入
  `Client.Screen` 的帧缓冲，`OnOrdersFrame` 报告变化区域。**默认关闭**，因为一旦
  广告 MEMBLT，服务端就**完全停止发位图更新**，两条路径**无法混用**。关着时服务端
  继续走位图更新 —— 正确，只是线上字节更多。

## 试用

`cmd/rdpcli` 是一个无头客户端：连接、解码，并把收到的内容写成 PNG。用来端到端检查
一台服务器足够了：

```sh
go run ./cmd/rdpcli -host 192.0.2.10:3389 -user user -pass secret -proto nla \
    -wait 20s -dump /tmp/screen.png

# 驱动输入并观察它是否生效
go run ./cmd/rdpcli -host 192.0.2.10:3389 -user user -pass secret -proto nla \
    -wait 20s -dump /tmp/after.png -post-input 4s \
    -key click:400,600 -type 'echo hello' -key press:0x1c
```

值得知道的参数：`-type` 键入字符串（会处理 Shift）；`-key` 发送原始输入事件；
`-clipboard` 与 `-set-clipboard` 用来验证剪贴板；`-egfx` 与 `-orders` 切换绘制路径；
`-pointer` 打印光标更新；`-log 0` 是完整跟踪日志。

## 验证是怎么做的

`docs/windows-verification.md` 记录了**对真实 Windows 主机检查过什么**，以及更有用的
**怎么检查的**：

* 哪种症状意味着**协议实现有问题**，哪种意味着**账号权限有问题**；
* 哪些问题上文本规范含糊、最终是**抓包定死的**；
* 以及过程中**我犯过哪些错**（这一节可能最有价值）。

编解码器与 ZGFX 都是**与 libfreerdp 逐字节比对**，而不是比对"我自己写的期望值"
—— 后者抓不到「解码器和测试用的是同一个误解」这类问题，而这类问题本次出现了**三次**。

## 开发

* `scripts/dev-rdp.sh` —— 管理本地 xrdp 靶机：`setup`（一次性，之后免密）、`up`、
  `down`、`status`、`probe`、`probe-session`（准备一个把 X 输入事件记录下来的会话，
  用来**验证输入**而不是靠截图猜）、以及 `standard`（把 xrdp 重启成要求
  Standard RDP Security，以便测试那条路径）。
* `scripts/gen-codec-vectors.sh` —— 用 libfreerdp 的编码器/解码器重新生成编解码器测试
  向量，使图像编解码器对齐**参照实现**，而不是手写期望值。
* `scripts/gen-zgfx-vectors.sh` —— ZGFX 同样处理；区别是 FreeRDP 的压缩器是个桩
  （只做原样拷贝），所以码流由我们的测试编码器产生，**参照输出仍由 FreeRDP 解码器给出**。
* `scripts/compare-shots.py` —— 比较**同一屏幕在不同绘制路径下**的两张截图，并区分
  "有损编码噪声"与"整块没画"。**务必同时跑同路径两次作为对照**，否则测到的是屏幕
  自身的变化（比如 Windows 壁纸轮播的淡入淡出）。

## 想法来源

* [rdpy](https://github.com/citronneur/rdpy)
* [node-rdpjs](https://github.com/citronneur/node-rdpjs)
* [gordp](https://github.com/Madnikulin50/gordp)
* [ncrack_rdp](https://github.com/nmap/ncrack/blob/master/modules/ncrack_rdp.cc)
