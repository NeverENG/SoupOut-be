// Package room 实现游戏房间（T0004 的 Room 接口实现）：
// 粘合 sim（规则）+ territory（地盘场）+ proto（协议编解码）。
// 每房间一局 4 人，第 4 人 OnJoin 时开局；soup.PlayerID 0..3 ↔ sim id 1..4。
// 协议适配（见 AGENTS.md）：快照走 SDK 保留号 msg=0（SDK 头 6B + room body）；
// 输入 user data 为 SDK 剥离 8B 头后的 20B。
package room

import (
	"sort"

	"soupout-server/internal/proto"
	"soupout-server/internal/sim"
	"soupout-server/internal/territory"
	"soupout-server/pkg/fixed"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// Config 是房间静态配置（cmd/server 注入）。
type Config struct {
	MapID     uint16
	GridW, GridH int
	StewTicks uint32 // 炖煮总时长（tick）
	Seed      uint64
}

// spawnCells 是四角出生格（与 territory 测试的种子布局一致，A0001M09 单图）。
var spawnCells = [4][2]int{{24, 24}, {72, 24}, {24, 72}, {72, 72}}

// GameRoom 是一局游戏。
type GameRoom struct {
	cfg  Config
	g    *sim.Game
	field *territory.Field

	joined  [4]bool
	started bool
	ended   bool

	terrSince [4]uint32 // 每玩家地盘 ACK（输入回传的 lastRecvTerritoryTick）
	terrTick  uint32    // 地盘帧计数（每 2 逻辑 tick 发一帧）
	keyframeAt uint32   // 上次 0x0C3 全量地盘帧的 tick
}

// NewGameRoom 创建房间（等待第 4 人触发开局）。
func NewGameRoom(cfg Config) *GameRoom {
	return &GameRoom{cfg: cfg}
}

// ---- soup.Room 接口 ----

func (r *GameRoom) OnJoin(ctx *soup.RoomCtx, p soup.PlayerID) {
	if int(p) >= len(r.joined) || r.started {
		ctx.Kick(p, 1)
		return
	}
	r.joined[p] = true
	if r.allJoined() {
		r.startMatch(ctx)
	}
}

func (r *GameRoom) OnLeave(ctx *soup.RoomCtx, p soup.PlayerID, why soup.LeaveReason) {
	if int(p) >= len(r.joined) {
		return
	}
	r.joined[p] = false
}

func (r *GameRoom) OnResume(ctx *soup.RoomCtx, p soup.PlayerID, gapMS uint32) {
	// 重连：SDK 会通过 keyframe 全量纠偏，room 侧无需额外处理。
}

func (r *GameRoom) OnInput(ctx *soup.RoomCtx, p soup.PlayerID, seq soup.InputSeq, payload []byte) {
	if r.started && !r.ended && int(p) < 4 {
		in, err := proto.DecodeInputUserData(payload)
		if err != nil {
			return // 坏帧丢弃（防注入）
		}
		r.terrSince[p] = in.LastRecvTerritoryTick
		r.g.SetInput(uint8(int(p)+1), sim.Input{
			MoveX:   in.Frames[0].MoveX,
			MoveY:   in.Frames[0].MoveY,
			Aim:     in.Frames[0].AimAngle,
			Buttons: in.Frames[0].Buttons,
			Has:     true,
		})
	}
}

func (r *GameRoom) Tick(ctx *soup.RoomCtx, tick soup.Tick, dtMS uint32) soup.Outcome {
	if !r.started || r.ended {
		return soup.Continue
	}
	r.syncTerrTick(ctx) // 0x0C1/0x0C2/0x0C3（Ch0/Ch2，在 sim 之前发出不影响推进）

	events := r.g.Step()
	r.broadcastEvents(ctx, events)
	r.broadcastScore(ctx)

	if r.g.Result.Ended {
		r.ended = true
		r.broadcastMatchEnd(ctx)
		return soup.End
	}
	return soup.Continue
}

func (r *GameRoom) EncodeSnapshot(target soup.PlayerID, baseline soup.Baseline, out *soup.Buffer) {
	// 本轮快照不做增量：全量 body（14B×4+1B，SDK 头 6B 已由框架写）。
	states := r.snapshotStates()
	proto.EncodeSnapshotBody(proto.BufferWriter{B: out}, states)
}

func (r *GameRoom) EncodeFullState(target soup.PlayerID, out *soup.Buffer) {
	g := r.g
	proto.EncodeFullState(proto.BufferWriter{B: out}, proto.FullState{
		ServerTick: g.Tick,
		StewRemain: r.stewRemainTicks(),
		Players:    r.fullStates(),
	})
}

func (r *GameRoom) StateHash() uint64 {
	return 0 // 回放校验（S4）落地时实现
}

// ---- 开局 ----

func (r *GameRoom) allJoined() bool {
	for _, j := range r.joined {
		if !j {
			return false
		}
	}
	return true
}

func (r *GameRoom) startMatch(ctx *soup.RoomCtx) {
	w, h := r.cfg.GridW, r.cfg.GridH
	field := territory.New(w, h, territory.Circle{R: 48})
	for i := 0; i < 4; i++ {
		field.Seed(uint8(i+1), territory.Cell(spawnCells[i][1]*w+spawnCells[i][0]), 724)
	}
	r.field = field
	r.g = sim.New(field, 50, r.cfg.Seed)
	for i := 0; i < 4; i++ {
		r.g.AddPlayer(uint8(i+1), cellCenterWorld(spawnCells[i]))
	}
	r.started = true

	// 0x040 MatchStart（Ch2 可靠有序）。地盘全量 keyframe 由 syncTerrTick 首帧
	// 以 tick=0（截至开局时刻的状态）发出：客户端 ACK=0 后 DiffSince(0) 能
	// 取到从第一条变更起的全部增量（完整性判定 since+1 >= oldest 恒成立）。
	b := ctx.BeginBroadcast(soup.ChReliableOrdered, proto.MsgMatchStart)
	proto.EncodeMatchStart(proto.BufferWriter{B: b}, r.cfg.MapID, 0, r.cfg.StewTicks, uint8(w), uint8(h), r.matchPlayers())
	ctx.Commit(b)
}

func (r *GameRoom) matchPlayers() []proto.MatchPlayer {
	players := make([]proto.MatchPlayer, 4)
	for i := range players {
		players[i] = proto.MatchPlayer{
			PlayerID: uint8(i + 1),
			Nickname: [16]byte{},
			SpawnX:   uint16(spawnCells[i][0]*100 + 50),
			SpawnY:   uint16(spawnCells[i][1]*100 + 50),
		}
	}
	return players
}

// cellCenterWorld 把出生格坐标换算为世界坐标（格中心，格单位 0..96）。
func cellCenterWorld(c [2]int) fixed.Vec2 {
	return fixed.Vec2{
		X: fixed.I(int32(c[0]*2 + 1)).Div(fixed.I(2)),
		Y: fixed.I(int32(c[1]*2 + 1)).Div(fixed.I(2)),
	}
}

// ---- tick 产出 ----

// syncTerrTick 每 2 逻辑 tick 发一帧地盘增量（10Hz，Ch0），
// 每 100 tick 发一次全量 keyframe（纠偏，Ch2 可靠有序）。
func (r *GameRoom) syncTerrTick(ctx *soup.RoomCtx) {
	r.terrTick++
	t := r.g.Tick + 1 // 地盘变更挂在本 tick（sim 尚未推进）
	// 首帧强制 keyframe（tick=0 状态）；此后每 100 tick 一次全量纠偏。
	if r.keyframeAt == 0 || t-r.keyframeAt >= 100 {
		r.sendTerritoryKeyframe(ctx, r.g.Tick)
		r.keyframeAt = t
		return
	}
	if r.terrTick%2 == 1 {
		for p := 0; p < 4; p++ {
			var changes []territory.Change
			complete := r.field.DiffSince(r.terrSince[p], &changes)
			if !complete {
				// 历史不足（客户端离线过久）：改发 keyframe 强制全量纠偏。
				r.sendTerritoryKeyframe(ctx, t)
				r.keyframeAt = t
				return
			}
			if len(changes) == 0 {
				continue // 无增量不发（客户端用上次状态）
			}
			b := ctx.BeginSend(soup.PlayerID(p), soup.ChUnreliable, proto.MsgTerritoryDelta)
			proto.EncodeTerritoryDelta(proto.BufferWriter{B: b}, t, r.terrSince[p], groupChanges(changes))
			ctx.Commit(b)
		}
	}
	if t-r.keyframeAt >= 100 {
		r.sendTerritoryKeyframe(ctx, t)
		r.keyframeAt = t
	}
}

// sendTerritoryKeyframe 发送 0x0C3 全量地盘帧（RLE，serverTick 由调用方指定）。
func (r *GameRoom) sendTerritoryKeyframe(ctx *soup.RoomCtx, tick uint32) {
	b := ctx.BeginBroadcast(soup.ChReliableOrdered, proto.MsgTerritoryKeyframe)
	proto.EncodeTerritoryKeyframeHeader(proto.BufferWriter{B: b}, tick, uint16(r.field.CountRLE()))
	r.field.EncodeRLE(proto.BufferWriter{B: b})
	ctx.Commit(b)
}

func (r *GameRoom) broadcastEvents(ctx *soup.RoomCtx, events []sim.Event) {
	for _, ev := range events {
		switch ev.Kind {
		case sim.EvPlayerDied:
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgPlayerDied)
			proto.EncodePlayerDied(proto.BufferWriter{B: b}, proto.PlayerDiedMsg{Victim: ev.A, Killer: ev.B, Tick: ev.Tick})
			ctx.Commit(b)
		case sim.EvPlayerRespawn:
			p := &r.g.Players[ev.A-1]
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgPlayerRespawn)
			proto.EncodePlayerRespawn(proto.BufferWriter{B: b}, proto.PlayerRespawnMsg{
				PlayerID: ev.A,
				PosX:     uint16(p.Pos.X.Mul(fixed.F(100)).ToInt()),
				PosY:     uint16(p.Pos.Y.Mul(fixed.F(100)).ToInt()),
				Tick:     ev.Tick,
			})
			ctx.Commit(b)
		case sim.EvPalletDown:
			pl := r.g.Pallets[ev.A-1]
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgPalletDown)
			proto.EncodePalletDown(proto.BufferWriter{B: b}, proto.PalletDownMsg{PalletID: ev.A, ByPlayer: ev.B, Tick: ev.Tick})
			_ = pl
			ctx.Commit(b)
		case sim.EvDropSpawn:
			// 掉落位置由 room 层按确定性规则生成（sim 只发 ID/type 事件）。
			x, y := r.dropPos(ev.A)
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgDropSpawn)
			proto.EncodeDropSpawn(proto.BufferWriter{B: b}, proto.DropSpawnMsg{DropID: ev.A, Type: ev.B, PosX: x, PosY: y})
			ctx.Commit(b)
		case sim.EvDropTaken:
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgDropTaken)
			proto.EncodeDropTaken(proto.BufferWriter{B: b}, proto.DropTakenMsg{DropID: ev.A, PlayerID: ev.B})
			ctx.Commit(b)
		case sim.EvVaultStart:
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgVaultStart)
			proto.EncodeVaultStart(proto.BufferWriter{B: b}, proto.VaultStartMsg{PlayerID: ev.A, VaultID: ev.B, DurationTicks: 8})
			ctx.Commit(b)
		case sim.EvVaultEnd:
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgVaultEnd)
			proto.EncodeVaultEnd(proto.BufferWriter{B: b}, proto.VaultEndMsg{PlayerID: ev.A})
			ctx.Commit(b)
		}
	}
}

// dropPos 确定性掉落位置：锅心附近伪随机（A0001M09 掉落规则联调时校准）。
func (r *GameRoom) dropPos(dropID uint8) (x, y uint16) {
	x = 4600 + uint16(dropID%7)*80
	y = 4600 + uint16(dropID/7%7)*80
	return
}

func (r *GameRoom) broadcastScore(ctx *soup.RoomCtx) {
	// 0x0C2 ScoreTick：与地盘帧同频（10Hz）。
	if r.terrTick%2 == 1 {
		b := ctx.BeginBroadcast(soup.ChUnreliable, proto.MsgScoreTick)
		proto.EncodeScoreTick(proto.BufferWriter{B: b}, r.g.Tick, r.g.AreaPermyriad)
		ctx.Commit(b)
	}
}

func (r *GameRoom) broadcastMatchEnd(ctx *soup.RoomCtx) {
	g := r.g
	players := make([]proto.MatchEndPlayer, 4)
	for i := 0; i < 4; i++ {
		players[i] = proto.MatchEndPlayer{
			PlayerID:     uint8(i + 1),
			Rank:         g.Result.Ranks[i],
			AreaPermyriad: g.AreaPermyriad[i],
			Kills:        uint8(g.Players[i].DeathCount), // 展示用，不参与排名
		}
	}
	b := ctx.BeginBroadcast(soup.ChReliableOrdered, proto.MsgMatchEnd)
	proto.EncodeMatchEnd(proto.BufferWriter{B: b}, players)
	ctx.Commit(b)
}

// ---- 快照 ----

func (r *GameRoom) snapshotStates() []proto.PlayerState {
	states := make([]proto.PlayerState, 0, 4)
	g := r.g
	for i := range g.Players {
		p := &g.Players[i]
		states = append(states, proto.PlayerState{
			PlayerID:   uint8(i + 1),
			PosX:       uint16(p.Pos.X.Mul(fixed.F(100)).ToInt()),
			PosY:       uint16(p.Pos.Y.Mul(fixed.F(100)).ToInt()),
			VelX:       int8(p.Vel.X.Mul(fixed.F(100)).ToInt()),
			VelY:       int8(p.Vel.Y.Mul(fixed.F(100)).ToInt()),
			AimAngle:   uint16(p.Aim.Mul(fixed.F(65536)).Div(fixed.TwoPi).ToInt()),
			Mass:       uint16(p.Mass.Mul(fixed.F(100)).ToInt()),
			StateFlags: stateFlags(g, i),
			HP:         uint8(p.HP),
		})
	}
	return states
}

func (r *GameRoom) fullStates() []proto.FullPlayer {
	fs := make([]proto.FullPlayer, 0, 4)
	g := r.g
	for i := range g.Players {
		p := &g.Players[i]
		fs = append(fs, proto.FullPlayer{
			PlayerID:    uint8(i + 1),
			PosX:        uint16(p.Pos.X.Mul(fixed.F(100)).ToInt()),
			PosY:        uint16(p.Pos.Y.Mul(fixed.F(100)).ToInt()),
			Aim:         uint16(p.Aim.Mul(fixed.F(65536)).Div(fixed.TwoPi).ToInt()),
			Mass:        uint16(p.Mass.Mul(fixed.F(100)).ToInt()),
			StateFlags:  stateFlags(g, i),
			HP:          uint8(p.HP),
			DeathCount:  uint8(p.DeathCount),
		})
	}
	return fs
}

func stateFlags(g *sim.Game, i int) uint8 {
	p := &g.Players[i]
	var f uint8
	if g.Charging[i] {
		f |= proto.FlagCharging
	}
	if p.Dead {
		f |= proto.FlagDead
	}
	if p.InvulnMS > 0 {
		f |= proto.FlagRespawnIM
	}
	if p.AttackWindupMS > 0 {
		f |= proto.FlagAttackWind
	}
	return f
}

func (r *GameRoom) stewRemainTicks() uint32 {
	elapsed := uint32(r.g.ElapsedMS / 50)
	if elapsed >= r.cfg.StewTicks {
		return 0
	}
	return r.cfg.StewTicks - elapsed
}

// ---- 地盘增量分组 ----

// groupChanges 把变更日志按 owner 分组，每组 cellIndex 升序（差值 varint 的前提）。
func groupChanges(changes []territory.Change) []proto.TerritoryGroup {
	groups := make([]proto.TerritoryGroup, 0, 5)
	var cur proto.TerritoryGroup
	flush := func() {
		if len(cur.Cells) > 0 {
			sort.Slice(cur.Cells, func(i, j int) bool { return cur.Cells[i] < cur.Cells[j] })
			groups = append(groups, cur)
		}
	}
	for i, c := range changes {
		if i == 0 || c.Owner != cur.Owner {
			flush()
			cur = proto.TerritoryGroup{Owner: c.Owner}
		}
		cur.Cells = append(cur.Cells, uint32(c.Cell))
	}
	flush()
	return groups
}
