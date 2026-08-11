# SoupOut 本地端到端联机

> 目标：一台机器上把 **Godot 客户端（旧线格式）→ BanNet 引擎 → Go 逻辑服** 全链路跑通。
> 已验证（2026-08-11，`tools/e2e_multi.sh`）：4 个客户端进锅 → 快照 4 人齐、tick 推进 →
> 地盘 keyframe/delta 落地且 ACK 闭环（keyframe 回落到每 100 tick）→ 各自看得见另外 3 人移动 →
> 带窗口跑截图 + 像素采样确认「脚下汤色 = 自己」。

## 两条验证路径（别只看冒烟）

| 脚本 | 探针 | 判据 | 用途 |
|---|---|---|---|
| `tools/e2e_local.sh` | `tests/online_probe.gd` | 收到 1 帧快照即 PASS | 连通性冒烟 |
| `tools/e2e_multi.sh` | `tests/online_hold_probe.gd` | 进局后驻留 15s：**4 人 · 快照 tick 推进 · 地盘 auth tick 推进 · ≥2 个远端玩家位置变化 ·（带窗口时）脚下汤色 = 自己** | 联机验收 |

⚠️ 冒烟脚本的判据只有「快照 tick > 0」。它在地盘完全没落地、所有玩家坐标缩了 10 倍、
没有任何人能动的情况下依然全绿 —— 这三个缺陷就是靠 `e2e_multi.sh` 才暴露的（见 §修复记录）。

```bash
bash tools/e2e_multi.sh              # 无头 4 客户端验收
VISUAL=1 bash tools/e2e_multi.sh     # 带窗口 4 客户端 + 各自截图到 /tmp/soup-e2e/shotN.png
SOUP_DEBUG=1 ./soup-server -socket … # 逻辑服联调日志（输入到达 / 地盘帧 / 位置推进）
```

## 架构与兼容模式

旧 Godot 客户端（`SoupOut-fe` 当前版本）的 UDP 线格式与 BanNet 规范有差异，BanNet 侧通过
**legacy 兼容开关**适配，客户端零改动：

| 差异点 | 规范（默认） | SoupOut P0（legacy） | 开关位置 |
|---|---|---|---|
| 帧头位序 | `msg_id<<4 \| ch` | `ch<<12 \| msg_id` | 引擎 `SessionTableConfig.frame_order=ChHigh` |
| 握手 | challenge-response 三次交换 | 首个握手包直建会话 | 引擎 `plaintext_handshake=true` |
| 每包 HMAC | 开 | 关（P0 明文） | 引擎 `enable_hmac=false` |
| 输入 0x080 | 28B（SDK 头 8B + user data 20B） | 30B 旧布局，游戏侧 `InputCodec` 转 20B | 游戏仓库 `adapter.LegacyInputCodec` |
| 快照 msg | SDK 保留号 0 | 0x0C0（body 14B/玩家） | SDK `WithSnapshotMsgID` |
| 全量 msg | SDK 保留号 1 | 0x042 | SDK `WithFullStateMsgID` |
| 地盘差值 | zigzag LEB128 | plain LEB128 | `proto.EncodeTerritoryDelta` 按 T0001 编码 |

正式客户端可去掉游戏侧 codec 与消息号映射、恢复三次握手 + HMAC；引擎 `serve` 提供
`--hmac` / `--plaintext` 开关。

## 启动

```bash
# 1. 起 Go 逻辑服（监听 UDS，等待引擎拨号）
cd SoupOut-be/server
go run ./cmd/server -socket /tmp/soup.sock

# 2. 起 BanNet 引擎（默认 UDP 0.0.0.0:12345，legacy 兼容模式）
cd BanNet
cargo run --release --example serve -- --bind 0.0.0.0:12345 --uds /tmp/soup.sock \
  --hmac false --plaintext true --frame-order chhigh

# 3. 开 4 个 Godot 客户端（P0 单房间：第 4 人进房自动开局）
cd SoupOut-fe
godot --path .        # 主菜单 → 快速匹配（需要 4 个客户端，才能开局）
```

> `e2e_multi.sh` 与上面的手工步骤等价，但**先编译再启动**、并等 UDS 真的出现才拉引擎
> （`go run` 冷启动 >1s，`e2e_local.sh` 的 `sleep 1` 会让引擎拨到还不存在的 socket）。

## 无头验证（4 客户端自动进锅）

```bash
# 在 BanNet 构建 serve 后（cargo build --example serve）：
cd SoupOut-be/server && bash tools/e2e_multi.sh   # 联机验收（推荐）
cd SoupOut-be/server && bash tools/e2e_local.sh   # 仅连通性冒烟
```

## 修复记录（2026-08-11：让联机真的可玩）

冒烟全绿但实际不可玩，三个缺陷：

1. **Ch2 分片头两侧不一致 → 地盘全量帧一条都没到客户端。**
   引擎 `src/reliable/fragment.rs` 用 6B 头 `group u16 · first_seq u16 · frag_no u8 · total u8`，
   客户端 `udp_transport.gd` 按 4B `frag_id u16 · idx u8 · cnt u8` 解 → `frag_cnt` 解成垃圾被判畸形。
   0x0C3 keyframe 有 1125B（>引擎 1100B 分片阈值）→ 永远丢弃 → 客户端 `last_auth_tick` 恒 0 →
   输入回传的地盘 ACK 恒 0 → 服务端每 2 tick 改发全量 keyframe（带宽恶化，增量机制完全失效）。
   T0002M03F03 把分片格式留白，**以引擎实现为准**，已改客户端（收发两侧 + `MAX_FRAG_CNT` 8→64）；
   同时补上「未集齐的分片也要参与 ack 记账」，否则引擎重传队列永不裁剪。
2. **快照/全量的坐标·速度·角度量化写错 → 4 个玩家挤在原点附近、看起来谁都不动。**
   `uint16(p.Pos.X.Mul(fixed.F(100)).ToInt())` 里 `fixed.F(100)` 是**裸定点值 ≈0.098**（该用 `fixed.I(100)`），
   且 T0001M01F03 的位置量化是 **1/64** 不是 1/100 → 实际下发 ≈ 世界坐标 × 0.098（世界 28.5 发成 2）。
   现改用 `quantPos/quantVel/quantAngle`（room/game.go）。
3. **出生点把「格」当「世界单位」→ 2/3/4 号玩家出生在世界外被夹到角上。**
   `cellCenterWorld` 除的是 2，格边长是 0.5 世界单位（96 格 ↔ 48 单位）→ 应除 4。

配套：`internal/room/game.go` 加 `SOUP_DEBUG=1` 联调日志（输入到达 / 0x0C1·0x0C3 发送 / 位置与格数）。

## 当前 P0 边界

- 单房间模型：快速匹配/建房都进固定房间 `SOUP`，第 4 人触发开局，不做 ready 门槛。
- 房间码/选料/ready 只做了状态广播与记录，未做完整大厅匹配。
- HMAC 与三次握手暂未开启（旧客户端不支持），正式上线前需客户端补齐或切换正式客户端。
