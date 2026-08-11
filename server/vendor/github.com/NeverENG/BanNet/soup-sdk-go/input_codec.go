package soup

import (
	"encoding/binary"
	"errors"
)

// errInputShort 是输入帧长度不足的错误。
var errInputShort = errors.New("soup: input frame too short")

// InputCodec 把 Ch1 输入帧解析成框架需要的字段 + 交给房间的 user data。
//
// BanNet 不知道游戏客户端怎么编排输入字节;不同游戏可以用不同布局。
// 默认提供 canonical(8B 头 + user data);游戏侧通过 WithInputCodec 注入自己的解析器,
// 解析逻辑放在游戏仓库,不进框架。
type InputCodec interface {
	// Decode 解析一条 Ch1 帧体。
	// 返回:
	//   clientTick            抖动缓冲排序用
	//   seq                   输入序号(去重/连续交付)
	//   lastRecvSnapshotTick  客户端最后收到的快照 tick(基线选择)
	//   userData              交给 Room.OnInput 的数据(仅本次调用有效)
	Decode(payload []byte) (clientTick uint32, seq InputSeq, lastRecvSnapshotTick Tick, userData []byte, err error)
}

// canonicalInputCodec 是默认输入编解码:
//
//	[clientTick u32][inputSeq u16][lastRecvSnapshotTick u16][user data]
type canonicalInputCodec struct{}

func (canonicalInputCodec) Decode(payload []byte) (uint32, InputSeq, Tick, []byte, error) {
	if len(payload) < inputHeaderLen {
		return 0, 0, 0, nil, errInputShort
	}
	clientTick := binary.LittleEndian.Uint32(payload[0:4])
	seq := InputSeq(binary.LittleEndian.Uint16(payload[4:6]))
	lastSnap := Tick(binary.LittleEndian.Uint16(payload[6:8]))
	return clientTick, seq, lastSnap, payload[inputHeaderLen:], nil
}

// inputCodecOrDefault 返回配置里的 codec,未配置时用 canonical。
func (c *Config) inputCodecOrDefault() InputCodec {
	if c.InputCodec != nil {
		return c.InputCodec
	}
	return canonicalInputCodec{}
}
