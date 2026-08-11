// Package adapter 放「游戏客户端 ↔ BanNet 框架」之间的适配实现。
// BanNet 核心不知道游戏客户端的字节布局,只提供 InputCodec 接口;
// SoupOut 旧 Godot 客户端的 30B 输入解析就在这里,不进框架。
package adapter

import (
	"encoding/binary"
	"errors"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// LegacyInputCodec 解析旧 Godot 客户端的 30B 输入帧,转成框架 user data(20B)。
//
// 旧布局(客户端 codec.encode_player_input 直写整帧):
//
//	clientTick u32 · inputSeq u16 · frameCount u8
//	· frameCount×(moveX i8 · moveY i8 · aim u16 · buttons u8)
//	· lastRecvSnapshotTick u32 · lastRecvTerritoryTick u32
//
// 转交给房间的 user data 与规范模式一致:
//
//	frameCount u8 · 3×(moveX · moveY · aim · buttons) · lastRecvTerritoryTick u32
type LegacyInputCodec struct{}

var errLegacyInputShort = errors.New("adapter: legacy input frame too short")

func (LegacyInputCodec) Decode(payload []byte) (uint32, soup.InputSeq, soup.Tick, []byte, error) {
	if len(payload) < 7 {
		return 0, 0, 0, nil, errLegacyInputShort
	}
	clientTick := binary.LittleEndian.Uint32(payload[0:4])
	seq := soup.InputSeq(binary.LittleEndian.Uint16(payload[4:6]))
	frameCount := int(payload[6])
	if frameCount > 3 {
		frameCount = 3
	}
	off := 7
	if off+frameCount*5+8 > len(payload) {
		return 0, 0, 0, nil, errLegacyInputShort
	}
	frames := payload[off : off+frameCount*5]
	off += frameCount * 5
	lastSnap := soup.Tick(binary.LittleEndian.Uint32(payload[off : off+4]))
	lastTerr := binary.LittleEndian.Uint32(payload[off+4 : off+8])

	user := make([]byte, 0, 20)
	user = append(user, byte(frameCount))
	user = append(user, frames...)
	user = append(user,
		byte(lastTerr), byte(lastTerr>>8), byte(lastTerr>>16), byte(lastTerr>>24))
	return clientTick, seq, lastSnap, user, nil
}
