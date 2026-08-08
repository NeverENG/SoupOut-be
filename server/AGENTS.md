# SoupOut server · Go 逻辑服 工程规范

> 依据：`../docs/be/T0004SoupServerGo.md`（实现架构）· `../docs/be/T0003SoupSDKGo.md`（SDK 契约）· `../docs/T0001SoupOut.md`（协议冻结）· `../docs/D0001SoupOutBalance.md`（数值唯一真相）。
> **框架（Rust 引擎 + Go SDK）已完成，位于 `../../BanNet`（soup-engine + soup-sdk-go），本仓库只写游戏逻辑，不重复造框架。**
> 所有数值以 `D0001` 为准，协议字节以 `T0001M02` 为准，改动须同步文档。

## 架构总览

```
SoupOut-be/
├── be/                  服务端规格文档（工作区副本；权威在 ../docs/be）
├── docs/                （上级目录）全部文档权威副本
└── server/              本游戏逻辑服 module soupout-server（游戏逻辑专用）
    ├── cmd/server          主程序：装配 SDK + Gatekeeper + Room
    ├── cmd/botclient       headless 假客户端
    ├── internal/territory  96×96 网格 · 增量测地膨胀 · 面积统计 · 增量导出（✅ W1）
    ├── internal/sim        移动 · 战斗 · 死亡复活 · 板子翻窗 · 道具 · 质量派生
    ├── internal/proto      T0001M02 全部消息编解码
    ├── internal/lobby      Gatekeeper：房间码池 · 快速匹配队列
    ├── internal/room       游戏 Room 实现：粘合 sim + territory + proto
    ├── pkg/fixed           定点数（Q22.10）（✅ W1）
    └── testdata/replays    确定性回放录像

框架仓库（../../BanNet）：
    soup-engine    Rust 引擎：UDP 可靠层 / 四通道 / 会话重连 / HMAC / 限流 / fuzz
    soup-sdk-go    Go SDK：Room 接口 / RoomCtx / Buffer 池 / tick 循环 / 抖动缓冲
                  / baseline 增量 / 重连补全量 / 确定性回放 / 零分配验收（Pong 已含）
```

依赖方向：`cmd/server → room → sim → territory → fixed`；`room → proto → fixed`。
**`territory` 只允许 import `pkg/fixed`（独立可测）。`sim` 不 import `proto`。`proto` 不 import `sim`。`room` 是唯一粘合层。**
`server` require `github.com/NeverENG/BanNet/soup-sdk-go`（go.mod replace => ../../../BanNet/soup-sdk-go）。

## 硬约束（违反即返工，来自 BE0000M05 / T0004M05）

1. **禁止把游戏状态建模成实体列表**：同步对象是连续区域场，SDK/框架只认「某客户端某 tick 要一段字节」。
2. **`sim` 与 `territory` 内禁止浮点**：全线 `pkg/fixed`（Q22.10）；Chamfer 距离用整数（正交 +8、对角 +11）。
3. **房间内不得遍历 map、不得起 goroutine、不得用 `time.Now` / `math/rand`**：确定性回放的前提。随机只用 `ctx.Rand()`（DetRand，seed 播种）。
4. **`DiffSince` 禁止全格扫描**：用环形变更日志 + 复用 bitset 去重。
5. **热路径零分配**：`Tick` + `EncodeSnapshot` 稳态 0 次堆分配（BanNet Pong 有 CI 断言；本游戏 room 同样遵守）。
6. **堆元素打包 `uint32 = dist<<16 | cell`**（territory 内部）；平局顺序确定。
7. **扩张必须在移动之后**；**战斗用上一 tick 的质量属性**。Tick 顺序见 `T0004M04`。
8. **解码不 panic**：任何输入先长度校验，长度不符返回错误并计数。

## 命令

```bash
cd server && go vet ./... && go test ./...   # 逻辑服全量（从仓库根 SoupOut-be 进入）
go test ./internal/territory/ -run 'TestT' -v   # territory 验收 T1–T8
cd ../../BanNet/soup-sdk-go && go test -race ./...   # 框架 SDK 全量（从仓库根进入）
```

## 引擎联调（serve 入口）

BanNet 引擎目前无独立 serve binary（engineload/interop 是引擎+客户端一体）。
在 BanNet 加 `examples/serve.rs`（约 10 行）后 `cargo run --release --example serve -- --uds /tmp/soup.sock`：

```rust
//! serve —— 仅启动引擎，等待 Go 逻辑服连接（本游戏联调用）。
use soup_engine::{Engine, EngineConfig};

fn main() {
    let mut args = std::env::args().skip(1);
    let mut uds = "/tmp/soup.sock".to_string();
    while let Some(k) = args.next() {
        if k == "--uds" { if let Some(v) = args.next() { uds = v; } }
    }
    let rt = tokio::runtime::Runtime::new().unwrap();
    rt.block_on(async {
        let engine = Engine::new(EngineConfig { uds_path: uds.into(), ..Default::default() }).unwrap();
        engine.run().await.unwrap();
    });
}
```

## 里程碑状态（T0004M07）

| 里程碑 | 状态 |
|---|---|
| W1 `pkg/fixed` + `internal/territory`（T1–T8） | ✅ 完成 |
| 框架（Rust 引擎 + Go SDK S1–S4） | ✅ BanNet 完成（Pong/回放/零分配/engineload 压测在案） |
| `internal/proto`（T0001M02 全部消息编解码） | ✅ 完成（8 测试，Writer 接口化 + 零分配解码） |
| `internal/sim`（D0001 规则引擎） | ✅ 完成（7 测试对照数值：LUT/伤害矩阵/复活阶梯/溶解速率） |
| `internal/room` + `internal/lobby` + `cmd/server` | ⬜ 当前主线 |
| `cmd/botclient` + 端到端（真引擎 + 4 客户端完整局） | ⬜ 需 serve 入口 |

## 与文档的已知偏差（有意为之）

- **协议澄清（T0001M02F04）**：`PlayerInput` 的帧字段为 moveX i8 · moveY i8 · aimAngle u16 · buttons u8 = **5 B/帧**，整包 4+2+1+3×5+4+4 = **30 B**。文档中的公式 `3×6 = 33 B` 与字段表矛盾（字段表为准）；带宽核算相应为 30 B × 20 Hz ≈ 0.6 KB/s。权威 `docs/T0001SoupOut.md` 待用户同步。
- **框架来源**：T0004 假设自研 SDK 与 mockengine；实际框架已完成于 `../../BanNet`（API 与 T0003 契约一致），本仓库不再包含 SDK/mockengine。
- `T0004M03F01` 给出 `EncodeRLE(out *proto.Buffer)`，但依赖图禁止 `territory` import `proto`。实际签名为 `EncodeRLE(w RLEWriter)`，`RLEWriter` 是 territory 内定义的最小接口（`PutU16`/`PutU8`），由 room 层用 `proto.Buffer` 实现对接。
- `T0004M03F01` 的 `blocked [4][]Cell` 实际存 `uint32`（打包 dist|cell），因为僵持格重入 frontier 时必须保留其 dist，只存 Cell 会丢距离。
- `T0004M03F01` 的 Field 增加 `inF [4]bitset`（frontier 成员标记），用于 push 去重 O(1)；claim 时清除该格在所有玩家位图中的位。
- **已知行为（非 bug，规格允许）**：抢占是几何过程，双方从两侧夹击可能挖穿突出部，产生「飞地」。规格 `T0004M03F01` 伪码没有防夹击机制；飞地随后会被对方从边缘自然蚕食，面积会计正确。若 P0 实测视觉不可接受，再加「空洞愈合」机制。
- **预分配不变量**：frontier/blocked 容量 2048、claimed 2×网格格数。超限会 panic（代替静默扩容，扩容 = 分配 = 破坏 T7 零分配）。R 增长 clamp 到 8192 Chamfer（保证 dist+19 < 65535，uint16 打包不溢出）。
- **`territory.StepRates`（T0004M03F01 的扩展）**：原 `Step(charging, rate, tick)` 是全局速率；为支持逐玩家道具倍率（润甜 ×2.5），新增 `StepRates(rates [4]fixed.F, tick)`（0 = 不充能），`Step` 变为兼容入口。sim.stepExpand 用 StepRates 接线润甜。
