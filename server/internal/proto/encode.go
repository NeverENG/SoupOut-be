package proto

import (
	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// Writer 是编码目标的最小接口。
// 与 territory.RLEWriter 同理：proto 不 import room 层，由调用方把
// *soup.Buffer 经 Wrap 适配（soup.Buffer 的 PutBytes 带 u16 长度前缀，
// 不适用于协议里的裸字节定长字段，故 PutRaw 由 Wrap 逐字节转发）。
type Writer interface {
	PutU8(v uint8)
	PutU16(v uint16)
	PutU32(v uint32)
	PutI16(v int16)
	PutVarint(v int64)
	PutRaw(p []byte)
}

// BufferWriter 把 *soup.Buffer 适配为 Writer（房间层 Encode* 用）。
type BufferWriter struct{ B *soup.Buffer }

func (w BufferWriter) PutU8(v uint8)     { w.B.PutU8(v) }
func (w BufferWriter) PutU16(v uint16)   { w.B.PutU16(v) }
func (w BufferWriter) PutU32(v uint32)   { w.B.PutU32(v) }
func (w BufferWriter) PutI16(v int16)    { w.B.PutI16(v) }
func (w BufferWriter) PutVarint(v int64) { w.B.PutVarint(v) }
func (w BufferWriter) PutRaw(p []byte) {
	for _, c := range p {
		w.B.PutU8(c) // 定长裸字段只出现在低频大厅/对局消息
	}
}

// Wrap 返回 *soup.Buffer 的 Writer 适配。
func Wrap(b *soup.Buffer) Writer { return BufferWriter{B: b} }

// ---- 大厅（S→C） ----

// EncodeRoomCreated 0x011：roomCode[4] · yourPlayerId u8。
func EncodeRoomCreated(w Writer, roomCode [RoomCodeLen]byte, playerID uint8) {
	w.PutRaw(roomCode[:])
	w.PutU8(playerID)
}

// EncodeJoinResult 0x013：code u8 · yourPlayerId u8。
func EncodeJoinResult(w Writer, code, playerID uint8) {
	w.PutU8(code)
	w.PutU8(playerID)
}

// RoomPlayer 是 RoomState 的 per-player 条目。
type RoomPlayer struct {
	PlayerID     uint8
	Nickname     [NicknameLen]byte
	IngredientID uint8
	Ready        uint8
}

// EncodeRoomState 0x017（广播）：roomCode[4] · n u8 · n×{playerId · nickname[16] · ingredientId · ready}。
func EncodeRoomState(w Writer, roomCode [RoomCodeLen]byte, players []RoomPlayer) {
	w.PutRaw(roomCode[:])
	w.PutU8(uint8(len(players)))
	for _, p := range players {
		w.PutU8(p.PlayerID)
		w.PutRaw(p.Nickname[:])
		w.PutU8(p.IngredientID)
		w.PutU8(p.Ready)
	}
}

// EncodeRoomClosed 0x01A：reason u8。
func EncodeRoomClosed(w Writer, reason uint8) { w.PutU8(reason) }

// ---- 对局控制（S→C） ----

// MatchPlayer 是 MatchStart 的 per-player 条目。
type MatchPlayer struct {
	PlayerID     uint8
	IngredientID uint8
	Nickname     [NicknameLen]byte
	SpawnX       uint16
	SpawnY       uint16
}

// EncodeMatchStart 0x040：mapId · startTick · stewTicks · gridW · gridH · n · n×{…}。
func EncodeMatchStart(w Writer, mapID uint16, startTick, stewTicks uint32, gridW, gridH uint8, players []MatchPlayer) {
	w.PutU16(mapID)
	w.PutU32(startTick)
	w.PutU32(stewTicks)
	w.PutU8(gridW)
	w.PutU8(gridH)
	w.PutU8(uint8(len(players)))
	for _, p := range players {
		w.PutU8(p.PlayerID)
		w.PutU8(p.IngredientID)
		w.PutRaw(p.Nickname[:])
		w.PutU16(p.SpawnX)
		w.PutU16(p.SpawnY)
	}
}

// MatchEndPlayer 是 MatchEnd 的 per-player 条目。
type MatchEndPlayer struct {
	PlayerID      uint8
	Rank          uint8
	AreaPermyriad uint16
	Kills         uint8
}

// EncodeMatchEnd 0x041：n u8 · n×{playerId · rank · areaPermyriad · kills}。
func EncodeMatchEnd(w Writer, players []MatchEndPlayer) {
	w.PutU8(uint8(len(players)))
	for _, p := range players {
		w.PutU8(p.PlayerID)
		w.PutU8(p.Rank)
		w.PutU16(p.AreaPermyriad)
		w.PutU8(p.Kills)
	}
}

// FullPlayer / Pallet / Drop 是 FullState 的条目（0x042）。
type FullPlayer struct {
	PlayerID      uint8
	PosX, PosY    uint16
	Aim           uint16
	Mass          uint16
	StateFlags    uint8
	HP            uint8
	DeathCount    uint8
	RespawnAtTick uint32
}

type Pallet struct {
	PalletID uint8
	State    uint8
}

type Drop struct {
	DropID uint8
	Type   uint8
	PosX   uint16
	PosY   uint16
}

// FullState 是 0x042 的完整消息。
type FullState struct {
	ServerTick uint32
	StewRemain uint32
	Players    []FullPlayer
	Pallets    []Pallet
	Drops      []Drop
}

// EncodeFullState 0x042：serverTick · stewRemain · n · n×{…} · palletCount · … · dropCount · …。
func EncodeFullState(w Writer, m FullState) {
	w.PutU32(m.ServerTick)
	w.PutU32(m.StewRemain)
	w.PutU8(uint8(len(m.Players)))
	for _, p := range m.Players {
		w.PutU8(p.PlayerID)
		w.PutU16(p.PosX)
		w.PutU16(p.PosY)
		w.PutU16(p.Aim)
		w.PutU16(p.Mass)
		w.PutU8(p.StateFlags)
		w.PutU8(p.HP)
		w.PutU8(p.DeathCount)
		w.PutU32(p.RespawnAtTick)
	}
	w.PutU8(uint8(len(m.Pallets)))
	for _, p := range m.Pallets {
		w.PutU8(p.PalletID)
		w.PutU8(p.State)
	}
	w.PutU8(uint8(len(m.Drops)))
	for _, d := range m.Drops {
		w.PutU8(d.DropID)
		w.PutU8(d.Type)
		w.PutU16(d.PosX)
		w.PutU16(d.PosY)
	}
}

// ---- 同步（S→C） ----

// EncodeSnapshot 0x0C0：serverTick · ackInputSeq · n · n×{playerId · posX · posY · velX · velY · aim · mass · flags · hp}。
// EncodeSnapshotBody 0x0C0 的 room 内容（SDK 快照头 6B 由框架写）：
// n · n×{playerId · posX · posY · velX · velY · aim · mass · stateFlags · hp}。
func EncodeSnapshotBody(w Writer, states []PlayerState) {
	w.PutU8(uint8(len(states)))
	for _, s := range states {
		w.PutU8(s.PlayerID)
		w.PutU16(s.PosX)
		w.PutU16(s.PosY)
		w.PutU8(uint8(s.VelX))
		w.PutU8(uint8(s.VelY))
		w.PutU16(s.AimAngle)
		w.PutU16(s.Mass)
		w.PutU8(s.StateFlags)
		w.PutU8(s.HP)
	}
}

// EncodeTerritoryDelta 0x0C1：serverTick · sinceTick · groupCount · 每 group{owner · cellCount · varint 差值序列}。
// cells 必须升序（room 层排序后传入）。
func EncodeTerritoryDelta(w Writer, serverTick, sinceTick uint32, groups []TerritoryGroup) {
	w.PutU32(serverTick)
	w.PutU32(sinceTick)
	w.PutU8(uint8(len(groups)))
	for _, g := range groups {
		w.PutU8(g.Owner)
		w.PutU16(uint16(len(g.Cells)))
		var prev uint32
		for _, c := range g.Cells {
			w.PutVarint(int64(c) - int64(prev)) // 差值（首元素 = 自身）
			prev = c
		}
	}
}

// EncodeScoreTick 0x0C2：serverTick · ratios[4]。
func EncodeScoreTick(w Writer, serverTick uint32, ratios [4]uint16) {
	w.PutU32(serverTick)
	for _, r := range ratios {
		w.PutU16(r)
	}
}

// EncodeTerritoryKeyframeHeader 0x0C3 的前半部分：serverTick · runCount。
// run 主体（length u16 · owner u8 序列）由 territory.EncodeRLE 经 RLEWriter 适配写入。
func EncodeTerritoryKeyframeHeader(w Writer, serverTick uint32, runCount uint16) {
	w.PutU32(serverTick)
	w.PutU16(runCount)
}

// ---- 事件（Ch3） ----

func EncodePlayerDied(w Writer, m PlayerDiedMsg) {
	w.PutU8(m.Victim)
	w.PutU8(m.Killer)
	w.PutU32(m.Tick)
}

func EncodePlayerRespawn(w Writer, m PlayerRespawnMsg) {
	w.PutU8(m.PlayerID)
	w.PutU16(m.PosX)
	w.PutU16(m.PosY)
	w.PutU32(m.Tick)
}

func EncodePalletDown(w Writer, m PalletDownMsg) {
	w.PutU8(m.PalletID)
	w.PutU8(m.ByPlayer)
	w.PutU32(m.Tick)
}

func EncodeDropSpawn(w Writer, m DropSpawnMsg) {
	w.PutU8(m.DropID)
	w.PutU8(m.Type)
	w.PutU16(m.PosX)
	w.PutU16(m.PosY)
}

func EncodeDropTaken(w Writer, m DropTakenMsg) {
	w.PutU8(m.DropID)
	w.PutU8(m.PlayerID)
}

func EncodeVaultStart(w Writer, m VaultStartMsg) {
	w.PutU8(m.PlayerID)
	w.PutU8(m.VaultID)
	w.PutU8(m.DurationTicks)
}

func EncodeVaultEnd(w Writer, m VaultEndMsg) { w.PutU8(m.PlayerID) }
