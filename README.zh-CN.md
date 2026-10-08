# Golang 远程桌面协议（RDP）客户端

grdp 是一个**纯 Go** 实现的微软远程桌面协议客户端。

Fork 自 [tomatome/grdp](https://github.com/tomatome/grdp)，后者 fork 自
[icodeface/grdp](https://github.com/icodeface/grdp)。

> 英文版说明见 [README.md](README.md)。真机验证的**方法**与**发现过程**记录在
> [docs/windows-verification.md](docs/windows-verification.md)，那是本仓库最值得读的一份文档。

## 文档

* [docs/porting-guide.md](docs/porting-guide.md) —— **在这个库上写东西**的人看：从连接到断开的整个会话、
  每一部分对你有什么要求，以及那些**已经付过一次代价**的坑。
* [docs/protocol-layers.md](docs/protocol-layers.md) —— **移植或扩展**它的人看的层次图：每个包做什么、
  不做什么，每层依赖下一层提供什么，以及出问题时**先看哪里**。
* [docs/windows-verification.md](docs/windows-verification.md) —— 对真实 Windows 主机**检查了什么、怎么检查的**，
  含那些值得再认一次的症状。
* [docs/input-verification.md](docs/input-verification.md) —— 如何**证明**输入到达了，而不是靠截图猜。

每个包都有包文档，每个导出标识符都有注释；`go doc ./client` 是个合适的起点。

## 状态

连接、认证、画面渲染、输入、剪贴板均已实现，并且**已在真实服务器上验证** ——
既走传统位图路径，也走较新的图形通道。

已对 **xrdp 0.9.24** 和一台**真实 Windows 10** 主机验证：

* [x] 连接、MCS、能力交换
* [x] TLS 与 NLA（CredSSP + NTLMv2，含 **版本 6** 的公钥绑定）
* [x] **Standard RDP Security**（无 TLS 的老路径）—— 已对本地 xrdp 实测通过；
      `scripts/dev-rdp.sh standard` 可把 xrdp 配置成要求它。它在同一目标上的输出与 TLS 路径
      **逐像素相同**，这比"能连上"强得多
* [x] **Display Control：运行中改分辨率** —— 通道由客户端主动申请，布局要**等服务器的能力值**
      之后再发（先发会被静默丢弃），`OnResize` 报告服务器**实际**采用的尺寸（它会凑到自己的显示模式：
      要 720 会得到 768），帧缓冲随之改变。已在**真实 Windows 10** 上验证：桌面真的改了，
      且两条合成路径在新尺寸下仍然 `0 of 983040` 像素差异。
* [x] 许可（Licensing）交换
* [x] 位图更新、RLE、24/32bpp
* [x] 光标位置，以及把光标形状**解码成可绘制的光标** —— AND/XOR 掩码已应用；
      能力集里宣告的 **20 个缓存槽真的实现了**，所以 CACHED 引用能取回形状而不是让光标消失；
      系统光标会如实标记；网关可从 `OnCursor` 拿到带 alpha 的 RGBA。
      已在 xrdp 上验证：它发的是双色光标，取回的热点值是真实光标该有的
      （箭头 `0,0`、工字 `4,8`），而不是垃圾值；并在**真实 Windows 10** 上验证：
      41×39 的箭头与工字分别有 145 到 352 个不透明像素
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

## 登录失败长什么样

登录失败是**分类过**的 ✓ —— 因为光看那句报错**分不出是哪种** ✓，而最常见的那一种**最误导人** ✗：

对着要求 NLA 的服务器，**密码错**不会以"认证失败"回来 ✗ —— Windows **不回答**错误的登录 ✓，它直接**断开 TLS** ✓ —— 所以拿到的是 `tls: internal error` ✓，看起来**像证书问题** ✗，会把运维引到完全错误的方向 ✓。

| 发生了什么 | 错误 | 怎么判定 |
| --- | --- | --- |
| 密码被拒 | 包装了 `ErrAuthenticationFailed` | `errors.Is(err, client.ErrAuthenticationFailed)` |
| 根本连不上 | 包装了 `ErrUnreachable` | `errors.Is(err, client.ErrUnreachable)` |
| ……以及原因 | `*net.OpError` 仍在错误链里 | `ECONNREFUSED` = 没人监听；`EHOSTUNREACH` = 网络不通 |
| TLS 握手失败（凭据还没发出） | 包装了 `ErrTLSFailure` | `errors.Is(err, client.ErrTLSFailure)` |
| CredSSP 交换失败（凭据还没发出） | 包装了 `ErrCredSSP` | `errors.Is(err, client.ErrCredSSP)` |
| 服务器要求的**安全层不同** | `*x224.NegotiationFailure`（带 code） | `errors.As(err, &x224.NegotiationFailure{})` |
| **服务器结束了会话**（通常是被别的连接接管） | 包装了 `ErrSessionEndedByServer` | `errors.Is(err, client.ErrSessionEndedByServer)` |

```sh
$ rdpcli -host host -user user -pass wrong -proto nla
login failed: client: the server did not accept the credentials: nla: the CredSSP
              authentication exchange failed: read header: remote error: tls: internal error
  kind: authentication — the exchange did not complete, which is usually a rejected password
```

**涉及账号的四种，是按「失败发生在哪一步」分开的** ✓ —— 因为这是传输层唯一能确定的事 ✓。
**凭据发出之前**失败 → `ErrTLSFailure` 或 `ErrCredSSP` ✓ —— 此时账号**还没被证伪** ✓；
**凭据发出之后**服务器沉默 → `ErrAuthenticationFailed` ✓ —— 这就是密码被拒在这里的样子 ✓。

`ErrAuthenticationFailed` 说的是「**服务器没有回应凭据**」✓，**故意不说「密码错」**✗ —— 因为**交换中途消失的服务器长得一模一样** ✓。

> `ErrTLSFailure` 与 `ErrCredSSP` **没有从真实服务器上见到过** ✗ —— 它们有单测、也走得到 ✓，
> 但手上没有服务器会产生它们 ✓，所以**不声称已验证** ✓。

最后一行那种**最像故障、其实不是** ✓：被别的连接接管的会话**还在服务器上** ✓（只是被挂起 ✓），**重连就取回** ✓。网络断了**根本不会**发断开通知 ✓ —— 所以「有没有收到通知」本身就是判据 ✓，**这是事实，不是在读字节** ✓。

**唯一完全没有信号的情况** ✗：服务器**接受连接**、然后把失败**画在会话里**（xrdp 就是这样 ✓ —— 它显示一个 `login failed for user` 对话框 ✓，线上什么都不发 ✓）。客户端**没有办法**察觉 ✓ —— 而"去看屏幕"不是这个库会做的事 ✓。

## 无头网关示例

`cmd/rdpws` 是一个单文件网关：它持有一个 RDP 会话，把画面按脏矩形编码成 JPEG 经 WebSocket
发给浏览器，并把浏览器的键盘、鼠标与光标收回来。这正是本库原来缺的示例——原有的两个示例
一个是桌面程序、一个是早期的 socket.io 页面，都没展示怎么在**没有窗口**的情况下驱动会话。

```sh
go run ./cmd/rdpws -host 192.0.2.10 -user user -pass secret
# 然后打开 http://127.0.0.1:8080/
```

它同时是**可以跑、而不是只能读**的测试：

```sh
go run ./cmd/rdpws -host 127.0.0.1 -user rdptest -pass rdptest -selftest
selftest: ok — page has a canvas, size announced, 12881 bytes as JPEG (1024x768),
                2 cursor images, 1 frames encoded
```

它会连接、启动服务、用浏览器会用的那条 WebSocket 驱动一帧画面和一张光标图、解码它们，
再发一个输入事件回去。`Framebuffer`/`OnFrame`、`OnCursor`、`TypeText` 这些网关需要的能力都集中在一个能运行的地方，
而不是分散在几处描述里。加上 `-resize WxH` 它还会**请求服务器改分辨率** ——
这正是浏览器页面最大化时需要的能力，也正是 Display Control 存在的理由。

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
| Display Control | 真实 Windows 10 按请求改了桌面，帧缓冲随之到 1280x768 |
| Windows 上的光标 | 真实 Windows 10：41×39、热点落在箭头尖端，并暴露了 xrdp 的双色光标永远不会走到的 AND 掩码规则 |
| 健壮性 | 模糊测试（抓到 2 个死循环、3 个 panic；语料已留存）|
| 形状/字形/九宫格/多矩形订单 | **仅**依据 FreeRDP 解析器 + 单测：没有可达服务端会发它们 |
| AVC420 / AVC444 分帧 | **仅**依据 FreeRDP 解析器 + 单测：解码需要本库不自带的解码器 |

## 作为库使用

```go
s := client.NewSetting()
s.Width, s.Height = 1024, 768
s.Protocol = "nla"           // "tls"、"nla" 或 "rdp"

c := client.NewClient("host:3389", "user", "password", client.TC_RDP, s)

// 像素只有一个落点，无论服务端选择哪条绘制路径。
c.OnFrame(func(dirty []image.Rectangle) { /* 只编码这些区域 */ })
c.OnCursor(func(cur *client.Cursor) { /* 按 cur.Hotspot 摆放并绘制 cur.Image */ })
c.OnCursorPos(func(x, y int) { /* 光标放这里 */ })
c.OnClipboardText(func(text string) { /* 粘贴 */ })

if err := c.Login(); err != nil {
    log.Fatal(err)
}

fb := c.Framebuffer()        // BGRA、自上而下，有效期到下一次 OnFrame
c.MouseMove(400, 300)
c.MouseDown(0, 400, 300)
c.MouseUp(0, 400, 300)
c.TypeText("中文", 8*time.Millisecond)  // 发的是字符，所以输入法能用
```

`OnFrame` 每个更新批次只回调一次 ✓，脏矩形**已经合并并裁剪好** ✓ —— 调用方编码"变了的部分" ✓，
不需要自己判断 ✓。像素从 `Framebuffer` 取 ✓，无论服务端走的是哪条路径 ✓。

**光标不属于桌面图像** ✓ —— `OnCursor` 给的是**带 alpha 的图像**加上**热点**（用来定位 ✓），
而且**只在形状变化时**触发 ✓、移动时不触发 ✓。

**按需开启的选项**（默认全关 ✓ —— 因为每一个都会改变协商内容 ✓）：

* `Setting.EnableClipboard` —— 打开剪贴板通道 ✓，文本双向；`Client.Files` 是文件传输接口 ✓
  （接收文件用 provider ✓，读服务器提供的文件**按区间读** ✓）
* `Setting.EnableDisplayControl` —— 打开 Display Control ✓，`RequestResize` 可在会话运行中改分辨率 ✓。
  结果从 `OnResize` 回来 ✓，而且**以结果为准** ✓：服务器会凑到它自己的显示模式 ✓，要 720 可能给 768 ✓
* `Setting.EnableEGFX` —— 进入图形通道 ✓。开启后不受支持的编解码器意味着**黑屏**而不是降级 ✓，
  因为选了 EGFX 的服务端**完全停发位图更新** ✓
* `Setting.EnableOrders` —— 广告 MEMBLT 并渲染随后的绘图订单 ✓。它改变服务端的绘制方式 ✓，
  两条路径**不能混用** ✓

`OnBitmap`、`Screen`、`OnOrdersFrame`、`OnSurfaceFrame` 仍然可用 ✓（想拿原始更新或订单机制的话 ✓），
在 `Framebuffer`/`OnFrame` 已取代它们的地方标了 **deprecated** ✓。

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
