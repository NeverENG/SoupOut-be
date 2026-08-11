// Package proto 实现 T0001M02 协议契约的全部消息编解码（小端）。
// 依赖方向：proto 只依赖 soup-sdk-go（Buffer），不 import sim/territory。
// 编码写入 soup.Buffer（房间层直接 Commit）；解码从 []byte 读出，输入热路径零分配。
package proto

import (
	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// ---- MsgID（T0001M02F01 段位分配） ----

const (
	// 大厅（Ch2，0x010–0x03F）
	MsgCreateRoom       = 0x010 // C→S
	MsgRoomCreated      = 0x011 // S→C
	MsgJoinRoom         = 0x012 // C→S
	MsgJoinResult       = 0x013 // S→C
	MsgQuickMatch       = 0x014 // C→S
	MsgLeaveRoom        = 0x016 // C→S
	MsgRoomState        = 0x017 // S→C 广播
	MsgSelectIngredient = 0x018 // C→S
	MsgSetReady         = 0x019 // C→S
	MsgRoomClosed       = 0x01A // S→C

	// 对局控制（Ch2，0x040–0x07F）
	MsgMatchStart = 0x040 // S→C
	MsgMatchEnd   = 0x041 // S→C
	MsgFullState  = 0x042 // S→C 重连/纠偏

	// 输入（Ch1，0x080–0x0BF）
	MsgPlayerInput = 0x080 // C→S

	// 同步（Ch0，0x0C0–0x0FF；0x0C3 走 Ch2）
	MsgSnapshot          = 0x0C0 // S→C
	MsgTerritoryDelta    = 0x0C1 // S→C
	MsgScoreTick         = 0x0C2 // S→C
	MsgTerritoryKeyframe = 0x0C3 // S→C

	// 事件（Ch3，0x100–0x13F）
	MsgPlayerDied    = 0x100
	MsgPlayerRespawn = 0x101
	MsgPalletDown    = 0x102
	MsgDropSpawn     = 0x103
	MsgDropTaken     = 0x104
	MsgVaultStart    = 0x105
	MsgVaultEnd      = 0x106
)

// ChannelOf 返回消息 ID 对应的通道（T0001M02F01）。
func ChannelOf(msg uint16) soup.Channel {
	switch {
	case msg >= 0x010 && msg <= 0x03F:
		return soup.ChReliableOrdered
	case msg >= 0x040 && msg <= 0x07F:
		return soup.ChReliableOrdered
	case msg >= 0x080 && msg <= 0x0BF:
		return soup.ChInput
	case msg >= 0x0C0 && msg <= 0x0FF:
		if msg == MsgTerritoryKeyframe {
			return soup.ChReliableOrdered
		}
		return soup.ChUnreliable
	case msg >= 0x100 && msg <= 0x13F:
		return soup.ChReliableUnordered
	default:
		return soup.ChUnreliable // 调试/预留
	}
}

// JoinResultCode 是 JoinResult 的 code（T0001M02F02）。
const (
	JoinOK       uint8 = 0
	JoinNotExist uint8 = 1
	JoinFull     uint8 = 2
	JoinStarted  uint8 = 3
)

// ---- 消息结构（字节布局对齐 T0001M02） ----

// NicknameLen 是昵称固定长度。
const NicknameLen = 16

// RoomCodeLen 是房间码固定长度（A-Z0-9 4 字符）。
const RoomCodeLen = 4

// PlayerInputFrame 是单帧输入（0x080 per frame）。
type PlayerInputFrame struct {
	MoveX    int8 // -100..100
	MoveY    int8
	AimAngle uint16 // 0..65535 → 0..2π
	Buttons  uint8  // bit0=攻击 bit1=充能扩张 bit2=交互(板/窗) bit3=技能(预留)
}

// Buttons 位定义（T0001M02F04）。
const (
	BtnAttack    uint8 = 1 << 0
	BtnExpand    uint8 = 1 << 1
	BtnInteract  uint8 = 1 << 2
	BtnSkill     uint8 = 1 << 3
)

// InputUserData 是 0x080 PlayerInput 的 user data（SDK 剥离 8B 输入头
// [clientTick u32 · inputSeq u16 · lastRecvSnapshotTick u16] 后交付 room 层，
// 见 AGENTS.md 协议适配记录）。
type InputUserData struct {
	FrameCount            uint8 // 固定 3（冗余）
	Frames                [3]PlayerInputFrame
	LastRecvTerritoryTick uint32 // 地盘 ACK 回传
}

// PlayerState 是 Snapshot per player（0x0C0）。
type PlayerState struct {
	PlayerID   uint8
	PosX       uint16
	PosY       uint16
	VelX       int8
	VelY       int8
	AimAngle   uint16
	Mass       uint16 // 由面积派生，服务器算好下发
	StateFlags uint8
	HP         uint8
	AtkCd10    uint8 // 攻击冷却剩余（单位 10ms），客户端冷却条用
}

// StateFlags 位定义（0x0C0）。
const (
	FlagCharging    uint8 = 1 << 0 // bit0=充能中
	FlagVaulting    uint8 = 1 << 1 // bit1=翻窗中
	FlagDead        uint8 = 1 << 2 // bit2=死亡
	FlagRespawnIM   uint8 = 1 << 3 // bit3=复活无敌
	FlagAttackWind  uint8 = 1 << 4 // bit4=攻击前摇
	FlagTooHeavy    uint8 = 1 << 5 // bit5=过重(禁板子)
)

// TerritoryGroup 是 TerritoryDelta per group（0x0C1）。
type TerritoryGroup struct {
	Owner     uint8 // 0=原汤 1..4=玩家
	CellCount uint16
	Cells     []uint32 // 差值解码后还原的 cellIndex（升序）
}

// 事件消息（0x100–0x106，Ch3）。
type PlayerDiedMsg struct {
	Victim uint8
	Killer uint8
	Tick   uint32
}

type PlayerRespawnMsg struct {
	PlayerID uint8
	PosX     uint16
	PosY     uint16
	Tick     uint32
}

type PalletDownMsg struct {
	PalletID uint8
	ByPlayer uint8
	Tick     uint32
}

type DropSpawnMsg struct {
	DropID uint8
	Type   uint8
	PosX   uint16
	PosY   uint16
}

type DropTakenMsg struct {
	DropID   uint8
	PlayerID uint8
}

type VaultStartMsg struct {
	PlayerID      uint8
	VaultID       uint8
	DurationTicks uint8
}

type VaultEndMsg struct {
	PlayerID uint8
}
