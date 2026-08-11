// Package room 实现游戏房间（T0004 的 Room 接口实现）：
// 粘合 sim（规则）+ territory（地盘场）+ proto（协议编解码）。
// 每房间一局 4 人，第 4 人 OnJoin 时开局；soup.PlayerID 0..3 ↔ sim id 1..4。
// 协议适配（见 AGENTS.md）：快照走 SDK 保留号 msg=0（SDK 头 6B + room body）；
// 输入 user data 为 SDK 剥离 8B 头后的 20B。
package room

import (
	"log"
	"math"
	"os"
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

	// 大厅状态(P0 最小实现:昵称/选料/ready,房间码固定 "SOUP")。
	nicknames   [4][proto.NicknameLen]byte
	ingredients [4]uint8
	ready       [4]bool

	terrSince [4]uint32 // 每玩家地盘 ACK（输入回传的 lastRecvTerritoryTick）
	terrTick  uint32    // 地盘帧计数（每 2 逻辑 tick 发一帧）
	keyframeAt uint32   // 上次 0x0C3 全量地盘帧的 tick（MaxUint32 = 尚未发过）

	dbgInputs [4]int // SOUP_DEBUG 联调计数：收到的输入帧数
}

// debugOn 打开联调日志（输入到达 / 地盘帧发送 / 位置推进）。
// 只在 SOUP_DEBUG=1 时输出，正常运行零开销之外零噪音。
var debugOn = os.Getenv("SOUP_DEBUG") == "1"

// NewGameRoom 创建房间（等待第 4 人触发开局）。
func NewGameRoom(cfg Config) *GameRoom {
	return &GameRoom{cfg: cfg, keyframeAt: math.MaxUint32}
}

// ---- soup.Room 接口 ----

func (r *GameRoom) OnJoin(ctx *soup.RoomCtx, p soup.PlayerID) {
	if int(p) >= len(r.joined) || r.started {
		ctx.Kick(p, 1)
		return
	}
	r.joined[p] = true
	log.Printf("room: player %d joined (joined=%v started=%v)", int(p)+1, r.joined, r.started)
	// 旧客户端靠 JOIN_RESULT 才知道自己的 player_id,必须在开局前先发。
	b := ctx.BeginSend(p, soup.ChReliableOrdered, proto.MsgJoinResult)
	proto.EncodeJoinResult(proto.BufferWriter{B: b}, proto.JoinOK, uint8(int(p)+1))
	ctx.Commit(b)
	r.broadcastRoomState(ctx)
	if r.allJoined() {
		r.startMatch(ctx)
	}
}

func (r *GameRoom) OnLeave(ctx *soup.RoomCtx, p soup.PlayerID, why soup.LeaveReason) {
	if int(p) >= len(r.joined) {
		return
	}
	r.joined[p] = false
	r.ready[p] = false
	if !r.started {
		r.broadcastRoomState(ctx)
	}
}

func (r *GameRoom) OnResume(ctx *soup.RoomCtx, p soup.PlayerID, gapMS uint32) {
	// 重连玩家地盘可能陈旧（增量按个人 ACK 补发）：立即全量 keyframe 纠偏。
	if r.started && !r.ended {
		r.sendTerritoryKeyframe(ctx, r.g.Tick)
		r.keyframeAt = r.g.Tick
	}
}

func (r *GameRoom) OnInput(ctx *soup.RoomCtx, p soup.PlayerID, seq soup.InputSeq, payload []byte) {
	if r.started && !r.ended && int(p) < 4 {
		in, err := proto.DecodeInputUserData(payload)
		if err != nil {
			if debugOn {
				log.Printf("dbg: OnInput p%d 解码失败 len=%d err=%v", int(p)+1, len(payload), err)
			}
			return // 坏帧丢弃（防注入）
		}
		if debugOn {
			r.dbgInputs[p]++
			if r.dbgInputs[p] <= 3 || r.dbgInputs[p]%100 == 0 {
				log.Printf("dbg: OnInput p%d #%d seq=%d frames=%d move=(%d,%d) btn=%d terrAck=%d",
					int(p)+1, r.dbgInputs[p], int(seq), in.FrameCount,
					in.Frames[0].MoveX, in.Frames[0].MoveY, in.Frames[0].Buttons,
					in.LastRecvTerritoryTick)
			}
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

// OnMessage 处理 Ch2/Ch3 业务消息(大厅/房间控制)。SDK 已把通道语义分开,
// 这里不再需要猜测 seq 是输入序号还是消息号。
func (r *GameRoom) OnMessage(ctx *soup.RoomCtx, p soup.PlayerID, msg soup.MsgID, payload []byte) {
	if int(p) >= 4 {
		return
	}
	switch msg {
	case proto.MsgCreateRoom, proto.MsgQuickMatch:
		if nick, err := proto.DecodeNickname(payload); err == nil {
			r.nicknames[p] = nick
		}
		// 玩家 ID 已在 OnJoin 用 JOIN_RESULT 发过;这里只更新昵称并广播房间状态。
		// ⚠️ 不能在此回 ROOM_CREATED:最后一名玩家会先收到 MATCH_START,
		// 再收到 ROOM_CREATED 会把已进局的客户端踢回房间页。
		r.broadcastRoomState(ctx)
	case proto.MsgJoinRoom:
		if _, nick, err := proto.DecodeRoomCodeAndNickname(payload); err == nil {
			r.nicknames[p] = nick
		}
		// JOIN_RESULT 已在 OnJoin 发过,同上不重复回。
		r.broadcastRoomState(ctx)
	case proto.MsgSelectIngredient:
		if v, err := proto.DecodeIngredient(payload); err == nil {
			r.ingredients[p] = v
			r.broadcastRoomState(ctx)
		}
	case proto.MsgSetReady:
		if v, err := proto.DecodeReady(payload); err == nil {
			r.ready[p] = v != 0
			r.broadcastRoomState(ctx)
		}
	case proto.MsgLeaveRoom:
		r.joined[p] = false
		r.ready[p] = false
		r.broadcastRoomState(ctx)
	}
}

// roomCode 是 P0 固定房间码(单房间模型;后续大厅实现后改为随机码池)。
var roomCode = [proto.RoomCodeLen]byte{'S', 'O', 'U', 'P'}

// broadcastRoomState 广播 0x017 房间状态(客户端房间屏的数据源)。
func (r *GameRoom) broadcastRoomState(ctx *soup.RoomCtx) {
	players := make([]proto.RoomPlayer, 0, 4)
	for i := range r.joined {
		if !r.joined[i] {
			continue
		}
		players = append(players, proto.RoomPlayer{
			PlayerID:     uint8(i + 1),
			Nickname:     r.nicknames[i],
			IngredientID: r.ingredients[i],
			Ready:        boolToU8(r.ready[i]),
		})
	}
	b := ctx.BeginBroadcast(soup.ChReliableOrdered, proto.MsgRoomState)
	proto.EncodeRoomState(proto.BufferWriter{B: b}, roomCode, players)
	ctx.Commit(b)
}

func boolToU8(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

func (r *GameRoom) Tick(ctx *soup.RoomCtx, tick soup.Tick, dtMS uint32) soup.Outcome {
	if !r.started || r.ended {
		return soup.Continue
	}
	r.syncTerrTick(ctx) // 0x0C1/0x0C2/0x0C3（Ch0/Ch2，在 sim 之前发出不影响推进）

	events := r.g.Step()
	r.broadcastEvents(ctx, events)
	r.broadcastScore(ctx)

	if debugOn && r.g.Tick%40 == 0 {
		log.Printf("dbg: tick=%d pos=[(%d,%d) (%d,%d) (%d,%d) (%d,%d)] cells=[%d %d %d %d] inputs=%v",
			r.g.Tick,
			r.g.Players[0].Pos.X, r.g.Players[0].Pos.Y,
			r.g.Players[1].Pos.X, r.g.Players[1].Pos.Y,
			r.g.Players[2].Pos.X, r.g.Players[2].Pos.Y,
			r.g.Players[3].Pos.X, r.g.Players[3].Pos.Y,
			r.field.Area(1), r.field.Area(2), r.field.Area(3), r.field.Area(4),
			r.dbgInputs)
	}

	if r.g.Result.Ended {
		r.ended = true
		r.broadcastMatchEnd(ctx)
		return soup.End
	}
	return soup.Continue
}

func (r *GameRoom) EncodeSnapshot(target soup.PlayerID, baseline soup.Baseline, out *soup.Buffer) {
	// 开局前(玩家未到齐)不产生快照:SDK 仍会写 6B 头,客户端按空包丢弃。
	if r.g == nil || !r.started {
		return
	}
	// 本轮快照不做增量：全量 body（13B×4+1B，SDK 头 6B 已由框架写）。
	states := r.snapshotStates()
	proto.EncodeSnapshotBody(proto.BufferWriter{B: out}, states)
}

func (r *GameRoom) EncodeFullState(target soup.PlayerID, out *soup.Buffer) {
	if r.g == nil || !r.started {
		return
	}
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
	log.Printf("room: start match")
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
		spawn := cellCenterWorld(spawnCells[i]) // 与 sim.AddPlayer 同一来源，两处不能各算一遍
		players[i] = proto.MatchPlayer{
			PlayerID: uint8(i + 1),
			Nickname: r.nicknames[i],
			SpawnX:   quantPos(spawn.X),
			SpawnY:   quantPos(spawn.Y),
		}
	}
	return players
}

// cellCenterWorld 把格坐标转成格心的世界坐标。
// 格边长 0.5 世界单位（96 格 ↔ 48 单位，T0001M01F03）→ world = (cell + 0.5) / 2 = (2c+1)/4。
// 曾误写作 /2（把格当成世界单位），导致 2/3/4 号玩家出生在世界外被 clampWorld 夹到角上。
func cellCenterWorld(c [2]int) fixed.Vec2 {
	return fixed.Vec2{
		X: fixed.I(int32(c[0]*2 + 1)).Div(fixed.I(4)),
		Y: fixed.I(int32(c[1]*2 + 1)).Div(fixed.I(4)),
	}
}

// ---- 线上量化（T0001M01F03：位置 u16 定点 1/64、角度 u16 映射 0..2π、速度 i8 定点 1/16） ----

// quantPos 世界坐标（Q22.10）→ u16 定点 1/64 单位，值域 0..3072。
func quantPos(v fixed.F) uint16 {
	q := (int64(v) * 64) >> fixed.FracBits
	if q < 0 {
		q = 0
	}
	if q > 0xFFFF {
		q = 0xFFFF
	}
	return uint16(q)
}

// quantVel 速度（Q22.10，单位/秒）→ i8 定点 1/16。
// ⚠️ 口径偏差：T0001M01F03 写的是「1/16 单位/**tick**」，但客户端 src/core/sim.gd
// 按「1/16 单位/**秒**」解（`vel * POS_SCALE / (VEL_SCALE * TICK_HZ)`），
// D0001M02 主属性表的移速列也是 单位/s。以客户端口径为准（i8 值域 ±7.9 单位/s 覆盖最大移速 6）。
func quantVel(v fixed.F) int8 {
	q := (int64(v) * 16) >> fixed.FracBits
	if q < -128 {
		q = -128
	}
	if q > 127 {
		q = 127
	}
	return int8(q)
}

// quantAngle 弧度（Q22.10）→ u16，0..65535 映射 0..2π。
func quantAngle(a fixed.F) uint16 {
	return uint16((int64(a) * 65536 / int64(fixed.TwoPi)) & 0xFFFF)
}

// ---- tick 产出 ----

// syncTerrTick 每 2 逻辑 tick 发一帧地盘增量（10Hz，Ch0），
// 每 100 tick 发一次全量 keyframe（纠偏，Ch2 可靠有序）。
func (r *GameRoom) syncTerrTick(ctx *soup.RoomCtx) {
	r.terrTick++
	t := r.g.Tick // 帧在 Step 前发出，serverTick = 内容截至的 tick（与 keyframe 语义一致）
	// 首帧强制 keyframe（tick=0 状态）；此后每 100 tick 一次全量纠偏。
	if r.keyframeAt == math.MaxUint32 || t-r.keyframeAt >= 100 {
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
			if debugOn && r.terrTick%40 == 1 {
				log.Printf("dbg: 0x0C1 delta → p%d tick=%d since=%d changes=%d payload=%dB",
					p+1, t, r.terrSince[p], len(changes), b.Len())
			}
			ctx.Commit(b)
		}
	}
}

// sendTerritoryKeyframe 发送 0x0C3 全量地盘帧（RLE，serverTick 由调用方指定）。
func (r *GameRoom) sendTerritoryKeyframe(ctx *soup.RoomCtx, tick uint32) {
	b := ctx.BeginBroadcast(soup.ChReliableOrdered, proto.MsgTerritoryKeyframe)
	runs := r.field.CountRLE()
	proto.EncodeTerritoryKeyframeHeader(proto.BufferWriter{B: b}, tick, uint16(runs))
	r.field.EncodeRLE(proto.BufferWriter{B: b})
	if debugOn {
		log.Printf("dbg: 0x0C3 keyframe tick=%d runs=%d payload=%dB", tick, runs, b.Len())
	}
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
				PosX:     quantPos(p.Pos.X),
				PosY:     quantPos(p.Pos.Y),
				Tick:     ev.Tick,
			})
			ctx.Commit(b)
		case sim.EvPalletDown:
			b := ctx.BeginBroadcast(soup.ChReliableUnordered, proto.MsgPalletDown)
			proto.EncodePalletDown(proto.BufferWriter{B: b}, proto.PalletDownMsg{PalletID: ev.A, ByPlayer: ev.B, Tick: ev.Tick})
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
	// mass 字段 = 面积万分比（客户端 match_state.area_permyriad_of 直接当万分比用，
	// T0001M02F05「由面积派生、服务器算好下发」）。
	ratios := r.field.Ratios()
	for i := range g.Players {
		p := &g.Players[i]
		states = append(states, proto.PlayerState{
			PlayerID:   uint8(i + 1),
			PosX:       quantPos(p.Pos.X),
			PosY:       quantPos(p.Pos.Y),
			VelX:       quantVel(p.Vel.X),
			VelY:       quantVel(p.Vel.Y),
			AimAngle:   quantAngle(p.Aim),
			Mass:       ratios[i],
			StateFlags: stateFlags(g, i),
			HP:         uint8(p.HP),
			AtkCd10:    uint8(p.AttackCDMS / 10),
		})
	}
	return states
}

func (r *GameRoom) fullStates() []proto.FullPlayer {
	fs := make([]proto.FullPlayer, 0, 4)
	g := r.g
	ratios := r.field.Ratios()
	for i := range g.Players {
		p := &g.Players[i]
		fs = append(fs, proto.FullPlayer{
			PlayerID:    uint8(i + 1),
			PosX:        quantPos(p.Pos.X),
			PosY:        quantPos(p.Pos.Y),
			Aim:         quantAngle(p.Aim),
			Mass:        ratios[i],
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
