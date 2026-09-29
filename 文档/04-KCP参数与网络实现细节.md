# 04 — KCP 参数与网络实现细节

> ## ⚠ 已作废声明（2026-08-14）
>
> 本文档为**早期 KCP 参数分析**。反汇编实证客户端实际使用 **TCP（TService）**：
> `NetKcpComponentAwakeSystem.Awake`（Unity.Model.dll Rva=0xE17C）`newobj ET.TService::.ctor`；
> KService（KCP）/KChannel/kcp.dll P/Invoke 在程序集内但**未被实例化/调用**。
> 以下内容仅作历史存档。现行实现见 `server/tcp_srv.go`（TCP 分帧）与 `server/msg.go`。

> 本文档基于 `参考数据/kchannel_dump.txt`（InitKcp/ctor/Update/HandleRecv/Output/Send）、
> `kservice_dump.txt`、`kservice2_dump.txt`、`session_dump.txt`（Kcp P/Invoke）还原。

## 1. KCP 原生接口（C 库 P/Invoke）

客户端通过 `kcp.dll`（P/Invoke）调用，`Kcp` 静态类封装（session_dump.txt）：

| 封装方法 | 底层 | 说明 |
|----------|------|------|
| `KcpCreate(conv, user)` | `ikcp_create(conv, user)` | user 为 `IntPtr(LocalConn)` |
| `KcpNodelay(nodelay, interval, resend, nc)` | `ikcp_nodelay` | 快速模式参数 |
| `KcpWndsize(sndwnd, rcvwnd)` | `ikcp_wndsize` | 收发窗口 |
| `KcpSetmtu(mtu)` | `ikcp_setmtu` | MTU |
| `KcpSetminrto(minrto)` | `ikcp_setminrto` | 最小 RTO |
| `KcpUpdate(current)` | `ikcp_update` | 时钟驱动 |
| `KcpCheck(current)` | `ikcp_check` | 返回下次需要更新的时间 |
| `KcpInput(buf, offset, len)` | `ikcp_input` | 喂入对端数据段 |
| `KcpRecv(buffer, len)` | `ikcp_recv` | 取出一段完整数据（长度须与缓存一致） |
| `KcpSend(buffer, offset, len)` | `ikcp_send` | 发送一段完整数据 |
| `KcpWaitsnd()` | `ikcp_waitsnd` | 未确认发送队列大小 |
| `KcpGetconv()` | `ikcp_getconv` | 读 conv（对端从 MSG 头读 conv 用） |

## 2. InitKcp 参数（反汇编确凿，RVA 0xF858）

按 `Service.ServiceType` 分支设置：

| 参数 | Outer (0) | Inner (1) |
|------|-----------|-----------|
| `KcpNodelay` | `(1, 10, 2, 1)` | `(1, 10, 2, 1)` |
| `KcpWndsize` | `(128, 128)` | `(102400, 102400)` |
| `KcpSetmtu` | `470` | `1400` |
| `KcpSetminrto` | `10` | `10` |

- `nodelay=1, interval=10, resend=2, nc=1`：低延迟模式（快速重传+无拥塞控制）。
- **Outer MTU=470**：客户端对网关流量，刻意压小 MTU 以适配小包高频；Go 侧实现须用同一 MTU 值，
  否则分片行为/包大小与客户端不一致。**kcp-go 默认 MTU=1400，需 SetMtu(470)**。
- `interval=10ms` 意味着 KCP 每 10ms 检查一次发送窗口；Go 侧 `kcp-go` 用 ticker 驱动 Update 即可。

> 服务器侧每个 accept channel 同样 `InitKcp()`，Outer 服务即用 Outer 参数（470/128/128）。

## 3. KService 结构（服务端侧关键字段）

| 字段 | 类型 | 说明 |
|------|------|------|
| `socket` | Socket | 单个 UDP socket，复用收发 |
| `cache` | byte[] | RecvFrom 接收缓冲 |
| `ipEndPoint` | IPEndPoint | RecvFrom 的 ref 出参（发送方地址） |
| `localConnChannels` | Dictionary\<uint, KChannel\> | **key = LocalConn**（服务器侧 S，客户端侧 C） |
| `kChannels` | Dictionary\<IntPtr, KChannel\> | **key = KCP user 指针（= LocalConn 的 IntPtr）**，KcpOutput 回调查表用 |
| `StartTime` | long | 启动时刻 |
| `TimeNow` | uint | `ClientNow() - StartTime`，KCP 时钟源 |

> `GetByLocalConn(conn)` = `localConnChannels.TryGetValue(conn)`。这就是 MSG 收包路由用的表。

## 4. KChannel 结构（关键字段）

| 字段 | 类型 | 说明 |
|------|------|------|
| `Id` | long | 通道 Id（`CreateConnectChannelId` / `CreateAcceptChannelId` 生成，见 05） |
| `LocalConn` / `RemoteConn` | uint | 本端/对端连接标识 |
| `IsConnected` | bool | ACK 握手完成 |
| `RealAddress` | IPEndPoint | 对端实际地址 |
| `Service` | KService | 所属服务 |
| `kcp` | IntPtr | KCP 实例句柄 |
| `sendCache` | byte[1048576] | 1MB 发送缓冲（KCP 段 + 5 字节前缀） |
| `sendBuffer` | Queue | 未连接时积压的待发消息 |
| `lastRecvTime` / `CreateTime` | long | 时间戳 |

## 5. Socket 收发模型

- **单个 UDP socket**：`ReceiveFrom` 从任意对端收包（`cache` + ref `ipEndPoint`），
  `SendTo` 指定目标地址发包。所有客户端共享同一 socket。
- 不按对端建 socket；按 `localConnChannels[conv]` 路由到具体 KChannel。
- UDP 是无连接的：RecvFrom 拿到的 `ipEndPoint` 就是该包来源（服务器可用它记录每个 channel 的 RealAddress）。
- Go 侧对应：`net.ListenUDP("udp", :7756)` + 单 goroutine `ReadFromUDP` 循环 + `WriteToUDP`。

> ET 框架在 Windows 下通常还会对 UDP socket 设置 `SIO_UDP_CONNRESET=false` 及较大收发缓冲，
> 本构建 dump 未含 socket 配置代码；Go 侧 UDP 天然不受 Windows ICMP 端口不可达影响，无需处理。

## 6. 发送与缓冲（KChannel.Send，RVA 0x100F8）

```
waitsnd = KcpWaitsnd()
if (ServiceType == Inner) { if (waitsnd > 1048576) → OnError(100211) }
else                      { if (waitsnd > 10240)   → OnError(100211) }
if (IsConnected)
    KcpSend(stream.GetBuffer(), stream.Position, stream.Length - Position)
else
    sendBuffer.Enqueue(stream)      // 未连接先排队，HandleConnnect 后补发
```

- **Outer 未确认发送队列上限 10240**，Inner 1048576；超限即断线（100211）。
- 未连接（SYN 握手完成前）的发送全部进 `sendBuffer` 队列，`HandleConnnect` 时逐条 `KcpSend` 补发。

## 7. 时钟与更新循环

- `TimeNow = ClientNow() - StartTime`（uint 毫秒），作为 KCP `current` 参数。
- `KService.Update` 遍历所有 channel 调 `channel.Update(TimeNow)`；`KChannel.Update`：
  - 未连接：超 10s（`CreateTime + 10000`）→ `OnError(100205)`；否则 Connect 型每 300ms 重发 SYN。
  - 已连接：`KcpUpdate(TimeNow)` → `KcpCheck(TimeNow)` 得下次更新时间 → `AddToUpdateNextTime`。
- `NetThreadComponent.LateUpdate` 每帧驱动所有 `Service.Update`（见 05 文档）。
- Go 侧：每个 KCP 实例每 10ms（interval）`Update` 一次；心跳/超时检查每帧做。

## 8. 错误码汇总（传输层）

| 错误码 | 触发场景 |
|--------|----------|
| `100205` | SYN 握手 10s 超时（未收到 ACK） |
| `100208` | 收到对端 FIN（对端主动断开） |
| `100209` | `SendTo` 抛异常（Socket 发送失败） |
| `100210` | `KcpUpdate` 抛异常（KCP 时钟异常） |
| `100211` | 发送队列超上限（Outer>10240 / Inner>1048576） |
| `100216` | 收到 MSG 但 conv 查不到 channel（服务器 Disconnect 用） |
| `10052` | `KcpRecv` peeksize==0（KCP 数据段异常） |

> 错误码通过 `channel.OnError` → `Service.OnError(Id, error)` 上抛到 Session。
> 服务器实现中，收到未知 conv 的 MSG 应回 FIN(100216)，见 02 文档。

## 9. 对自建服务器（Go）的关键映射

| ET 行为 | Go 侧对应 |
|---------|-----------|
| `ikcp_create(conv, user)` | kcp-go `NewKCP(conv, output)`，且 **conv 校验须禁用/自管**（02 文档 3.4） |
| MTU=470（Outer） | `kcp.SetMtu(470)` |
| `KcpNodelay(1,10,2,1)` | `kcp.SetNoDelay(1, 10, 2, 1)` |
| `KcpWndsize(128,128)` | `kcp.SetWindowSize(128, 128)` |
| `Setminrto(10)` | kcp-go `SetStreamMode` 无关；RTO 下限需自定义或接受默认 |
| `KcpWaitsnd` 上限 10240 | kcp-go `WaitSnd()` |
| `KcpUpdate` 每 10ms | kcp-go goroutine ticker 调 `Update(nowMs)` |
| KCP 输出回调 → Output | kcp-go 输出回调里拼 `[0x04][LocalConn][段]` 后 WriteToUDP |
