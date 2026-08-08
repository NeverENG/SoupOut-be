# SoupOut 逻辑服 · 协议适配记录（W2–3）

> 本文记录服务端 W2–3 里程碑（`internal/room` + `internal/lobby` + `cmd/server` + 集成测试）中，
> T0001 冻结协议（权威 `../docs/T0001SoupOut.md`）在 BanNet `soup-sdk-go`（vendor 于 `server/vendor/`，
> 含 2 处本地修复，见 §4）之上的落地方式：SDK 保留号 / SDK 头机制 / 各消息 wire 布局 / 联调方式。
> 所有事实均标注来源文件，未核实内容不写入。
>
> 速读：**快照走 SDK 保留号 msg=0（Ch0）**，**重连全量走保留号 msg=1（Ch2）**，
> **输入 28B = SDK 头 8B + user data 20B**；0x0C1 cells 必须升序；0x0C3 依赖客户端 ACK 回传。

## 1. 适配总览（T0001 消息号 → wire 消息号 / 通道 / SDK 头）

| T0001 消息 | T0001 号 | wire 通道 | wire 消息号 | SDK 头 | room 层实现 |
|---|---|---|---|---|---|
| 快照 Snapshot | 0x0C0 | ChUnreliable(0) | **msg=0**（SDK 保留 `MsgSnapshot`） | **6B**：`snapshotTick u32 · lastProcessedInputSeq u16` | `EncodeSnapshot` → `proto.EncodeSnapshotBody` |
| 重连全量 FullState | 0x042 | ChReliableOrdered(2) | **msg=1**（SDK 保留 `MsgFullState`） | 无 | `EncodeFullState` |
| 输入 PlayerInput | 0x080 | ChInput(1) | 0x080 | **8B**：`clientTick u32 · inputSeq u16 · lastRecvSnapshotTick u16`（SDK 剥离） | `OnInput` 收 20B user data → `proto.DecodeInputUserData` |
| 地盘增量 TerritoryDelta | 0x0C1 | ChUnreliable(0) | 0x0C1 | 无 | `proto.EncodeTerritoryDelta` |
| 比分 ScoreTick | 0x0C2 | ChUnreliable(0) | 0x0C2 | 无 | `proto.EncodeScoreTick` |
| 地盘全量 TerritoryKeyframe | 0x0C3 | ChReliableOrdered(2) | 0x0C3 | 无 | `proto.EncodeTerritoryKeyframeHeader` + `Field.EncodeRLE` |
| 事件 PlayerDied…VaultEnd | 0x100–0x106 | ChReliableUnordered(3) | 同号不变 | 无 | `broadcastEvents`（各 `Encode*`） |

来源：`server/vendor/.../soup-sdk-go/types.go`（保留号与头长度）、`server/internal/proto/messages.go`（消息号段位与 `ChannelOf`）、`server/internal/room/game.go`、`server/vendor/.../soup-sdk-go/baseline.go`、`server/vendor/.../soup-sdk-go/ticker.go`。

## 2. 各消息 wire 字节布局（小端）

### 2.1 快照（wire msg=0，Ch0）

完整 payload = **SDK 头 6B + room body**：

```
[snapshotTick u32][lastProcessedInputSeq u16]          ← SDK 写（baseline.go:54-56；types.go:57-58）
[n u8]                                                 ← room body 起始（encode.go:189）
n×[playerId u8 · posX u16 · posY u16 · velX i8 · velY i8
   · aimAngle u16 · mass u16 · stateFlags u8 · hp u8]  ← 13B/人（encode.go:189-202）
```

- SDK 按 `SnapshotHz`（`cmd/server` 注入 10Hz）逐玩家调度：`scheduleSnapshots` 先写 6B 头，再调 `room.EncodeSnapshot`（`server/vendor/.../soup-sdk-go/baseline.go:42-76`）。
- room 只写 body，不写头（game.go:109-113 注释「SDK 头 6B 已由框架写」）；本轮快照不做增量，`baseline` 参数未用。
- 集成测试按「msg=0 · 6B 头 + body（`payload[6] > 0`）」验证（integration_test.go:259-262）。
- 客户端在输入头 `lastRecvSnapshotTick` 回传最近收到的快照 tick，SDK 据此选增量基线（baseline.go:57-65，与 room 无关）。

### 2.2 重连全量（wire msg=1，Ch2）

- SDK 在 `inResume`（重连 / 中途加入）时自动 `BeginSend(ChReliableOrdered, MsgFullState)` 并调用 `room.EncodeFullState`，**无 SDK 头**（`server/vendor/.../soup-sdk-go/ticker.go:137-146`）。
- body（`proto.FullState`，encode.go:154-182；room 实现 game.go:115-122）：

```
serverTick u32 · stewRemain u32
· n u8 · n×{playerId u8 · posX u16 · posY u16 · aim u16 · mass u16
            · stateFlags u8 · hp u8 · deathCount u8 · respawnAtTick u32}
· palletCount u8 · … · dropCount u8 · …
```

### 2.3 输入（wire 0x080，Ch1）——28B = SDK 头 8B + user data 20B

客户端上行 payload：

```
[clientTick u32][inputSeq u16][lastRecvSnapshotTick u16]   ← SDK 头 8B（types.go:44-53，inputHeaderLen=8）
[frameCount u8]                                            ┐
3×[moveX i8 · moveY i8 · aimAngle u16 · buttons u8]        │ user data 20B
[lastRecvTerritoryTick u32]                                ┘
```

- SDK 在 `handleCh1Input` 解析 8B 头后只把 **user data 20B** 放入抖动缓冲，交付时 `OnInput` 收到 20B（`server/vendor/.../soup-sdk-go/jitter.go:150-174`）。
- room 用 `proto.DecodeInputUserData` 解码（decode.go:78-108）：`Frames[0]` 转 `sim.Input` 入推进，`LastRecvTerritoryTick` 存入 `terrSince[p]` 作地盘 ACK（game.go:74-89）。
- 构造示例见集成测试 `sendInputErr`：`8+1+2+8+20` 字节 DataUp body（integration_test.go:114-135）。
- **两个独立 ACK**：SDK 头内 `lastRecvSnapshotTick`（SDK 快照基线用）与 user data 内 `lastRecvTerritoryTick`（room 地盘 ACK 用），互不相干。
- 备注：AGENTS.md「已知偏差」记录的 T0001 文档公式 30B 是文档口径；wire 实为 28B（8B 头 + 20B user data），字段与 T0001M02F04 字段表一致。

### 2.4 地盘增量 0x0C1（Ch0）

```
[serverTick u32][sinceTick u32][groupCount u8]
每 group：[owner u8 · cellCount u16 · varint 差值序列]
```

- **cells 必须升序**：编码以 `prev` 为基准做差值（首元素差值 = 自身），升序是差值 varint 可正确解码的前提（encode.go:204-219）。
- 变更日志从新到旧（`forEachNewerThan` 从环尾向前），room 层 `groupChanges` 按 owner 分组后对每组 `sort.Slice` 升序（game.go:372-391）。
- `serverTick` = 地盘变更所属 tick：增量帧在 sim 推进**前**发出（game.go:95），取 `t = r.g.Tick + 1`（game.go:187）；`sinceTick` = 该玩家最近回传的 ACK（`r.terrSince[p]`，game.go:207-208）。
- 发送频率：每 2 逻辑 tick 一帧（10Hz，Ch0）（game.go:194）；该玩家无变更则不发送（game.go:204-205）。

### 2.5 地盘全量 0x0C3（Ch2）

```
[serverTick u32][runCount u16] + runCount×[length u16 · owner u8]
```

- `serverTick` 语义 = **「截至该 tick 的地盘状态」**；开局 keyframe 为 tick=0（开局时刻状态，game.go:152-154、189-192）。
- 发送时机（game.go:185-216）：首帧强制 keyframe；此后每 100 tick 全量纠偏；`DiffSince` 完整性失败时立即改发。
- **ACK 契约（客户端必须遵守）**：客户端须在输入 user data 的 `lastRecvTerritoryTick` 持续回传**最新收到的 keyframe / delta 的 serverTick**。否则 `DiffSince` 完整性判定失败（`sinceTick+1 < 环最老 tick`，`server/internal/territory/diff.go:66-76`）→ room 判定「历史不足」**每帧改发全量 keyframe**（game.go:197-203）→ 增量机制失效、带宽恶化。
- 集成测试按真实客户端行为回传 keyframe serverTick 作 ACK（integration_test.go:214-228、246-247）。

### 2.6 事件 0x100–0x106（Ch3）

- 字节布局与 T0001 一致，未做任何适配（各 `Encode*`：encode.go:238-275；消息结构：messages.go:141-179）。
- 通道：全部 **ChReliableUnordered(3)** 广播（game.go:226-269 `broadcastEvents`；messages.go:63-64 `ChannelOf` 0x100–0x13F 段位）。
- sim 只输出 `sim.Event`（不 import proto），由 room 层转换（sim/game.go:20-39；game.go:226-269）。

## 3. 地盘 ACK 与 DiffSince 完整性判定（0x0C3 回退链路）

```
输入 user data.lastRecvTerritoryTick ──► room.terrSince[p]（game.go:80）
        │
        ▼
TerritoryDelta 编码用 sinceTick = terrSince[p]（game.go:207-208）
        │
        ▼
field.DiffSince(terrSince[p])（territory/diff.go:69-87）
  ├─ since+1 >= 环最老 tick ──► 返回增量，正常发送
  └─ since+1 <  环最老 tick ──► 返回 false → room 改发全量 keyframe（game.go:197-203）
```

- 环形变更日志容量 4096 ≈ 40s 历史（diff.go:10-11）；客户端离线超过该窗口或 ACK 停止推进都会触发回退。
- 开局 keyframe tick=0 保证「客户端 ACK=0 → `DiffSince(0)` 恒完整」（game.go:152-154 注释）。

## 4. SDK 本地修复（server/vendor/）

- 背景：`go.mod` 用 `replace => ../../../BanNet/soup-sdk-go`（go.mod:7），`server/vendor/` 内有 SDK 快照副本（vendor/modules.txt）。默认 `-mod=vendor` 构建，本地修复随副本生效。
- **修复 1（排序括号，pend[-1] 越界）**：`jitter.go` `deliverReadyInputs` 的插入排序循环条件原为 `j > 0 && A || B && C`（`&&` 优先级高于 `||`），当 `j` 递减到 0 后 `(B && C)` 仍可能为真 → 继续比较/交换 `pend[-1]`，越界访问。修复为 `j > 0 && (A || (B && C))`（vendor jitter.go:129-134）。
- **修复 2（raw 归还推迟，data race）**：原版在交付循环内、`OnInput` 调用**之前**就 `readPool.Put(head.raw)`；读缓冲归还后可能被 `readLoop` 立即复用覆写，`OnInput` 解码会读到被覆盖的数据。修复：`pendingDeliver` 携带 `raw`，全部 `OnInput` 返回后统一归还（vendor jitter.go:18-24、135-145；ticker.go:92-95 的警告注释）。
- ⚠️ **提醒**：以上两处修复**尚未同步回 BanNet 原仓库**（`git diff` 对比 `../../../BanNet/soup-sdk-go/jitter.go` 可复现）。后续在 BanNet 侧合入，否则脱离 vendor 直接构建（如 CI 用 replace）会拿到未修复代码。

## 5. 联调方式

```bash
cd server
go run ./cmd/server -socket /tmp/soup.sock   # SDK 监听 UDS；引擎（soup-engine）拨号该路径
```

- 逻辑服入口：`cmd/server/main.go`（`-socket` 默认 `/tmp/soup.sock`，main.go:25-26）；SDK 是**单 accept**模型，等待引擎拨号（integration_test.go:97-98）。
- 引擎侧 serve 入口（仅启动引擎等待 Go 逻辑服）：见 AGENTS.md「引擎联调」，`examples/serve.rs --uds /tmp/soup.sock`。

### 帧速查表（UDS 帧协议，全部小端；帧头 = `len u32 · type u8` 共 5B）

来源：`server/vendor/.../soup-sdk-go/frame.go:12-29` 及各 parse 函数。

**引擎 → 逻辑服：**

| 帧 | type | body 布局 |
|---|---|---|
| EngineHello | 0x30 | `version u16 · caps u32`（6B，连接建立首帧） |
| SessionOpen | 0x01 | `sess_id u64 · addr[18] · token_len u16 · token[]` |
| SessionClose | 0x02 | `sess_id u64 · reason u8` |
| SessionResume | 0x03 | `sess_id u64 · gap_ms u32` |
| DataUp | 0x10 | `sess_id u64 · ch u8 · msg_id u16 · payload[]` |
| SessionStats | 0x20 | `sess_id u64 · rtt_ms u16 · loss_permille u16 · out_kbps u16` |
| Overload | 0x2F | `dropped_up u32 · dropped_down u32` |

**逻辑服 → 引擎：**

| 帧 | type | body 布局 |
|---|---|---|
| LogicHello | 0x90 | `version u16 · caps u32` |
| Send | 0x81 | `sess_id u64 · ch u8 · msg_id u16 · payload[]`（与 DataUp 同布局） |
| Multicast | 0x82 | `n u8 · sess_id[n] u64 · ch u8 · msg_id u16 · payload[]` |
| Kick | 0x83 | `sess_id u64 · reason u8` |
| SetBudget | 0x84 | `sess_id u64 · kbps u16` |

**自测参考**：`internal/room/integration_test.go` 的 `TestFullMatchFlow` 完整扮演引擎（握手 → 4×SessionOpen → MatchStart + 开局 keyframe → 持续输入与 ACK 回传 → 快照/地盘增量/比分校验）：

```bash
cd server && go test -race ./internal/room/ -run TestFullMatchFlow -v
```

## 6. 事实来源文件索引

| 事实 | 来源 |
|---|---|
| SDK 保留号 msg=0 / msg=1、inputHeaderLen=8、snapshotHeaderLen=6、四通道定义 | `server/vendor/.../soup-sdk-go/types.go:16-58` |
| SDK 写 6B 快照头、msg=0 ChUnreliable 调度、baseline 选择 | `server/vendor/.../soup-sdk-go/baseline.go:37-81` |
| 重连推 msg=1 FullState（无头） | `server/vendor/.../soup-sdk-go/ticker.go:132-146` |
| SDK 剥 8B 输入头、抖动缓冲交付 20B | `server/vendor/.../soup-sdk-go/jitter.go:150-174` |
| vendor 修复 1 / 修复 2 的修复后代码与注释 | `server/vendor/.../soup-sdk-go/jitter.go:129-134、135-145` |
| 快照 body / 全量 / 输入 user data / 0x0C1 / 0x0C3 / 事件布局 | `server/internal/proto/encode.go:186-275` |
| 输入 user data 解码（20B、零分配） | `server/internal/proto/decode.go:78-108` |
| 消息号与通道段位 | `server/internal/proto/messages.go:12-68` |
| room 层快照/全量/输入/地盘/事件实现 | `server/internal/room/game.go:74-89、109-122、185-269` |
| groupChanges 升序排序 | `server/internal/room/game.go:372-391` |
| DiffSince 完整性判定（since+1 < 环最老 tick → false） | `server/internal/territory/diff.go:64-87` |
| 帧协议常量与各帧 body 布局 | `server/vendor/.../soup-sdk-go/frame.go:12-29` |
| 联调入口与参数 | `server/cmd/server/main.go:25-33` |
| 集成测试帧格式验证 | `server/internal/room/integration_test.go:111-135、190-212、214-268` |
| 依赖方向与模块替换 | `server/internal/lobby/gatekeeper.go:7-11`、`server/internal/sim/game.go:7-10`、`server/go.mod:5-7` |
