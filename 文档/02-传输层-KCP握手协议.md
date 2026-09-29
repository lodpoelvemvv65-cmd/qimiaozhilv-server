# 02 — 传输层：KCP 握手协议

> ## ⚠ 已作废声明（2026-08-14）
>
> 本文档为**早期 KCP/UDP 分析**，已被反汇编实证推翻：客户端实际使用 **TCP（TService）**，
> 不存在 SYN/ACK/FIN/MSG 握手。反汇编证据：
> `NetKcpComponentAwakeSystem.Awake`（Unity.Model.dll Rva=0xE17C）→ `newobj ET.TService::.ctor`；
> `KService`（KCP）类在程序集内但**未被任何代码实例化**。
>
> **现行协议（以 `server/tcp_srv.go` 为准）**：
> ```
> TCP 帧 = [u16 LE 总长度(含 opcode)] [u16 LE opcode] [protobuf body]
> ```
> 无长度头之外的前缀、无握手包。以下内容仅作历史存档/对照参考。

> 本文档基于 `参考数据/kservice_dump.txt`（KService.Recv 完整反汇编）、
> `kservice2_dump.txt`、`kchannel_dump.txt`（Connect/HandleConnnect/Output）还原。
> 所有字节序均为 **Little-Endian**（Windows x86_64 + .NET `BitConverter` 默认）。

## 1. 协议类型常量（KcpProtocalType）

| 常量 | 值 | 方向/含义 |
|------|-----|-----------|
| SYN | `0x01` | 客户端 → 服务器，请求建立连接 |
| ACK | `0x02` | 服务器 → 客户端，确认连接 |
| FIN | `0x03` | 双向，断开连接（携带错误码） |
| MSG | `0x04` | 双向，KCP 数据载荷 |
| RouterReconnect | `0x0A` | 路由器重连 |
| RouterAck | `0x0B` | 路由器确认 |
| RouterSYN | `0x0C` | 路由器同步 |

> 客户端对外（网关）通信只用 SYN/ACK/FIN/MSG，路由器类型用于服务器间/内网穿透，客户端连接流程不涉及。

## 2. UDP 包格式

### SYN（客户端 → 服务器，共 9 字节）

```
offset  size  字段         值
0       1     type         0x01
1       4     LocalConn    C（客户端随机生成）
5       4     RemoteConn   0（客户端未知服务器）
```

客户端 `KChannel.Connect`（RVA 0xFCF0）写入：
```csharp
WriteTo(sendCache, 0, 1)          // type
WriteTo(sendCache, 1, LocalConn)  // C
WriteTo(sendCache, 5, RemoteConn) // 0
Socket.SendTo(sendCache, 0, 9, 0, RemoteAddress)
```
之后每 **300ms** 重发（`AddToUpdateNextTime(TimeNow+300, Id)`），10s 超时断开（error 100205）。

### ACK（服务器 → 客户端，共 9 字节）

```
offset  size  字段         值
0       1     type         0x02
1       4     LocalConn    S（服务器为本次连接分配的 LocalConn）
5       4     RemoteConn   C（客户端 LocalConn，原样回显）
```

客户端 Recv（kservice_dump.txt 0x0060 分支）：
```csharp
// 要求长度 == 9
localConn = BitConverter.ToUInt32(cache, 1);   // = S
remoteConn = BitConverter.ToUInt32(cache, 5);  // = C
channel = GetByLocalConn(remoteConn);          // 用 C 查 localConnChannels[C]
if (channel != null) {
    channel.set_RemoteConn(localConn);         // RemoteConn = S
    channel.HandleConnnect();                  // 建 KCP、置 IsConnected、清发缓冲
}
```

### FIN（双向，共 13 字节）

```
offset  size  字段        值
0       1     type        0x03
1       4     LocalConn   （发送方 LocalConn）
5       4     RemoteConn  （对端 LocalConn）
9       4     error       错误码 int32（如 100208=对端断开）
```

客户端 Recv（0x00CD 分支）：`GetByLocalConn(cache[5])` 找到 channel，
校验 `channel.RemoteConn == cache[1]`，然后 `channel.OnError(100208)`。

服务器发送 FIN 用 `Disconnect(localConn, remoteConn, error, ipEndPoint, n)`：
`[0x03][localConn u32][remoteConn u32][error i32]`，通过 socket.SendTo 发送，
**最多重发 3 次**（`Disconnect` 内循环 `arg5` 次）。

### MSG（双向，KCP 数据载荷）

```
offset  size  字段            值
0       1     type            0x04
1       4     LocalConn       （发送方 LocalConn）
5       n     KCP 数据        （KCP 协议段，从 offset 5 开始）
```

**发送方** `KChannel.Output`（RVA 0xFFE8，KCP 输出回调）：
```csharp
WriteTo(sendCache, 0, 4)          // type = MSG
WriteTo(sendCache, 1, LocalConn)  // 发送方 LocalConn
Marshal.Copy(kcpBuffer, 0, sendCache, 5, len);  // KCP 数据 → offset 5
Socket.SendTo(sendCache, 0, len + 5, 0, RemoteAddress);
```

**接收方** `KService.Recv`（0x0169 分支，要求长度 ≥ 9）：
```csharp
sender = BitConverter.ToUInt32(cache, 1);          // 发送方 LocalConn
conv   = BitConverter.ToUInt32(cache, 5);          // KCP 段前 4 字节 = conv 字段
channel = GetByLocalConn(conv);                    // ← conv 用于查 channel！
if (channel == null) Disconnect(conv, sender, 100216, ip, 1);
else if (channel.RemoteConn == sender)             // 校验发送方
    channel.HandleRecv(cache, 5, len - 5);         // KCP 数据从 offset 5 送入
```

## 3. conv 语义（关键成果）

### 3.1 各方标识

- **客户端 LocalConn = C**：`CreateRandomLocalConn()` = `0x40000000 | RandUInt32()`（高位置 1）
- **服务器 LocalConn = S**：服务器端同样 `CreateRandomLocalConn()` 生成
- **RemoteConn**：对端的 LocalConn

### 3.2 KCP 创建参数

| 端 | 调用 | conv 参数 | user 参数 |
|----|------|-----------|-----------|
| 客户端 CONNECT channel | `KcpCreate(RemoteConn, LocalConn)` | `conv = 0`（初始）→ 收到 ACK 后重建 | `user = C` |
| 客户端 HandleConnnect 后 | `KcpCreate(RemoteConn, LocalConn)` | `conv = S`（服务器 LocalConn） | `user = C` |
| 服务器 ACCEPT channel | `KcpCreate(RemoteConn, LocalConn)` | `conv = C`（客户端 LocalConn） | `user = S` |

即 **`conv = 对端 LocalConn`，`user = 本端 LocalConn`**。

### 3.3 conv 的唯一用途：收包路由

关键点：**两端 KCP conv 不同**（客户端 conv=S，服务器 conv=C），因此 **KCP 内部 conv 校验必然被 ET 禁用**（否则两端互不相认）。

conv 的**唯一用途**是 MSG 收包时做 channel 查找：

- 服务器收到 `[0x04][C][KCP conv=S]` → `GetByLocalConn(S)` 命中服务器 channel ✓
- 客户端收到 `[0x04][S][KCP conv=C]` → `GetByLocalConn(C)` 命中客户端 channel ✓

### 3.4 对自建服务器（Go）的含义

1. 每个 channel 一个独立的 KCP 实例；
2. 用 MSG 包 offset 5 的 4 字节（= 对端 LocalConn）做 channel 查找 key；
3. 发送 KCP 段时把该 channel 的 conv（= 客户端 C）写入段头，使 KCP 段 4 字节 conv == 客户端 LocalConn；
4. 由于两端 conv 不同，Go 侧 KCP 必须**禁用/忽略 conv 校验**（kcp-go 默认校验收发 conv 一致性，需要处理：要么自定义 conv 逻辑，要么发送 conv 与接收 conv 保持一致并自行管理，见 07 文档）。

## 4. 握手完整时序

```
客户端                          自建服务器 (UDP :7756)
  |                                  |
  |-- SYN [0x01][C][0] ------------>|  服务器分配 LocalConn=S，建 accept channel
  |                                  |  localConnChannels[S] = channel
  |<-- ACK [0x02][S][C] ------------|  回 ACK
  |                                  |
  |  客户端: set_RemoteConn(S)        |
  |  客户端: KcpCreate(conv=S,user=C) |
  |  客户端: IsConnected=true, 清队列 |
  |                                  |
  |-- MSG [0x04][C][KCP(conv=S)...]>|  KCP 握手(IKCP_CMD_WASK/ACK) + 业务
  |                                  |  服务器: KcpCreate(conv=C,user=S)
  |<-- MSG [0x04][S][KCP(conv=C)...] |
  |                                  |
  |-- MSG [0x04][C][KCP: opcode body]|
  |<-- MSG [0x04][S][KCP: opcode body]
```

注意：**服务器收到 SYN 不立即回 ACK**，而是创建 channel 后回 ACK；ACK 里 `[1]=S`（服务器 LocalConn）、`[5]=C`（回显客户端 LocalConn）。

## 5. KChannel 连接侧流程（客户端，反汇编确认）

### ctor（Connect channel，RVA 0xF908）
签名 `(long Id, uint LocalConn, uint RemoteConn, IPEndPoint RemoteAddress, KService Service)`：
1. 校验 `kChannels.ContainsKey(LocalConn)` 不重复；
2. `ChannelType = 0 (Connect)`；
3. `kcp = KcpCreate(RemoteConn, LocalConn.conv.u8)`（conv=0, user=C）；
4. `kChannels[LocalConn] = this`；
5. `lastRecvTime = CreateTime = now`；
6. 调 `Connect()` 发 SYN。

### HandleConnnect（RVA 0xFC0C，收到 ACK 后）
1. `kcp = KcpCreate(RemoteConn, LocalConn.conv.u8)`（**重建**，此时 conv=S, user=C）；
2. `InitKcp()`（设参数，见 04 文档）；
3. `idLocalRemoteConn[Id] = (RemoteConn << 32) | LocalConn`；
4. `IsConnected = true`；`lastRecvTime = now`；
5. 把 `sendBuffer` 队列里积压的消息全部 `KcpSend` 出去。

### Update（RVA 0xFDE4，KService 每帧调用）
- 未连接：超 10s（CreateTime+10000）→ OnError(100205)；否则（ChannelType==Connect）每 300ms 重发 SYN；
- 已连接：`KcpUpdate(now)`（KCP 自身时钟）；`KcpCheck` 得到下次需要更新的时间 → `AddToUpdateNextTime`。

## 6. 断开流程（FIN / 超时）

| 触发 | 客户端行为 |
|------|-----------|
| 收到对端 FIN | `OnError(100208)` → channel 从 `idChannels` 移除 → `Service.OnError(Id, 100208)` |
| SYN 10s 超时 | `OnError(100205)` |
| KcpUpdate 抛异常 | `OnError(100210)` |
| 本地主动断开 | 客户端发 FIN `[0x03][C][S][error]` |

> 错误码 1002xx 为传输层错误（100205/100208/100209/100210/100211/100216 等），
> 10052 为 KCP peeksize==0 异常。这些错误码会透传到上层 Session 的 error 处理。
