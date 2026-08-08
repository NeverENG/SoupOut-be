package proto

// 解码：从 []byte 读出（小端），全部带长度校验，不 panic（BE0000M05⑧）。
// 输入热路径（0x080 PlayerInput）返回值类型，零分配。

import (
	"encoding/binary"
	"errors"
)

var ErrShort = errors.New("proto: message truncated")

// reader 是带长度校验的字节读取器。
type reader struct {
	b   []byte
	off int
}

func (r *reader) u8() (uint8, bool) {
	if r.off+1 > len(r.b) {
		return 0, false
	}
	v := r.b[r.off]
	r.off++
	return v, true
}

func (r *reader) u16() (uint16, bool) {
	if r.off+2 > len(r.b) {
		return 0, false
	}
	v := binary.LittleEndian.Uint16(r.b[r.off : r.off+2])
	r.off += 2
	return v, true
}

func (r *reader) u32() (uint32, bool) {
	if r.off+4 > len(r.b) {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(r.b[r.off : r.off+4])
	r.off += 4
	return v, true
}

func (r *reader) i8() (int8, bool) {
	v, ok := r.u8()
	return int8(v), ok
}

// varint 读取 zigzag LEB128（与 PutVarint 对称）。
func (r *reader) varint() (int64, bool) {
	var u uint64
	for i := 0; i < 10; i++ {
		c, ok := r.u8()
		if !ok {
			return 0, false
		}
		u |= uint64(c&0x7F) << (7 * i)
		if c&0x80 == 0 {
			return int64(u>>1) ^ -int64(u&1), true
		}
	}
	return 0, false
}

func (r *reader) raw(n int) ([]byte, bool) {
	if r.off+n > len(r.b) {
		return nil, false
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v, true
}

// ---- 输入（C→S） ----

// DecodeInputUserData 解码 0x080 的 user data（20B，SDK 剥离 8B 头后交付）。
// 零分配热路径：frameCount u8 · 3×{moveX · moveY · aimAngle · buttons} · lastRecvTerritoryTick u32。
func DecodeInputUserData(body []byte) (InputUserData, error) {
	r := reader{b: body}
	var m InputUserData
	var ok bool
	if m.FrameCount, ok = r.u8(); !ok {
		return m, ErrShort
	}
	if m.FrameCount > 3 {
		m.FrameCount = 3 // 冗余字段，防御性裁剪
	}
	for i := 0; i < int(m.FrameCount); i++ {
		if m.Frames[i].MoveX, ok = r.i8(); !ok {
			return m, ErrShort
		}
		if m.Frames[i].MoveY, ok = r.i8(); !ok {
			return m, ErrShort
		}
		if m.Frames[i].AimAngle, ok = r.u16(); !ok {
			return m, ErrShort
		}
		if m.Frames[i].Buttons, ok = r.u8(); !ok {
			return m, ErrShort
		}
	}
	if m.LastRecvTerritoryTick, ok = r.u32(); !ok {
		return m, ErrShort
	}
	return m, nil
}

// ---- 大厅（C→S） ----

func DecodeNickname(body []byte) ([NicknameLen]byte, error) {
	var n [NicknameLen]byte
	r := reader{b: body}
	raw, ok := r.raw(NicknameLen)
	if !ok {
		return n, ErrShort
	}
	copy(n[:], raw)
	return n, nil
}

func DecodeRoomCodeAndNickname(body []byte) ([RoomCodeLen]byte, [NicknameLen]byte, error) {
	var code [RoomCodeLen]byte
	var nick [NicknameLen]byte
	r := reader{b: body}
	raw, ok := r.raw(RoomCodeLen)
	if !ok {
		return code, nick, ErrShort
	}
	copy(code[:], raw)
	raw, ok = r.raw(NicknameLen)
	if !ok {
		return code, nick, ErrShort
	}
	copy(nick[:], raw)
	return code, nick, nil
}

// DecodeIngredient 0x018 SelectIngredient。
func DecodeIngredient(body []byte) (uint8, error) {
	r := reader{b: body}
	v, ok := r.u8()
	if !ok {
		return 0, ErrShort
	}
	return v, nil
}

// DecodeReady 0x019 SetReady。
func DecodeReady(body []byte) (uint8, error) {
	return DecodeIngredient(body)
}

// ---- 同步（S→C，botclient 用） ----

// DecodeSnapshot 0x0C0：states 复用切片（容量不足时扩容）。
// DecodeSnapshotBody 解码 0x0C0 的 room 内容（SDK 头已剥离）。
func DecodeSnapshotBody(body []byte, states []PlayerState) (out []PlayerState, err error) {
	r := reader{b: body}
	n, err := mustU8(&r)
	if err != nil {
		return
	}
	out = states[:0]
	if cap(out) < int(n) {
		out = make([]PlayerState, 0, n)
	}
	for i := 0; i < int(n); i++ {
		var s PlayerState
		if s.PlayerID, err = mustU8(&r); err != nil {
			return
		}
		if s.PosX, err = mustU16(&r); err != nil {
			return
		}
		if s.PosY, err = mustU16(&r); err != nil {
			return
		}
		if s.VelX, err = mustI8(&r); err != nil {
			return
		}
		if s.VelY, err = mustI8(&r); err != nil {
			return
		}
		if s.AimAngle, err = mustU16(&r); err != nil {
			return
		}
		if s.Mass, err = mustU16(&r); err != nil {
			return
		}
		if s.StateFlags, err = mustU8(&r); err != nil {
			return
		}
		if s.HP, err = mustU8(&r); err != nil {
			return
		}
		out = append(out, s)
	}
	return
}

// DecodeTerritoryDelta 0x0C1：groups 复用（Cells 复用 group 内切片）。
func DecodeTerritoryDelta(body []byte, groups []TerritoryGroup) (serverTick, sinceTick uint32, out []TerritoryGroup, err error) {
	r := reader{b: body}
	if serverTick, err = mustU32(&r); err != nil {
		return
	}
	if sinceTick, err = mustU32(&r); err != nil {
		return
	}
	n, err := mustU8(&r)
	if err != nil {
		return
	}
	out = groups[:0]
	// Cells 每次分配（botclient 低频解码；room 层只编码不解码，热路径零分配不受影响）。
	for i := 0; i < int(n); i++ {
		var g TerritoryGroup
		if g.Owner, err = mustU8(&r); err != nil {
			return
		}
		cnt, err2 := mustU16(&r)
		if err2 != nil {
			err = err2
			return
		}
		g.Cells = make([]uint32, int(cnt))
		var prev uint32
		for j := 0; j < int(cnt); j++ {
			d, ok := r.varint()
			if !ok {
				err = ErrShort
				return
			}
			c := uint32(int64(prev) + d)
			g.Cells[j] = c
			prev = c
		}
		out = append(out, g)
	}
	return
}

// DecodeScoreTick 0x0C2。
func DecodeScoreTick(body []byte) (serverTick uint32, ratios [4]uint16, err error) {
	r := reader{b: body}
	if serverTick, err = mustU32(&r); err != nil {
		return
	}
	for i := 0; i < 4; i++ {
		if ratios[i], err = mustU16(&r); err != nil {
			return
		}
	}
	return
}

// KeyframeRun 是 0x0C3 的游程。
type KeyframeRun struct {
	Length uint16
	Owner  uint8
}

// DecodeTerritoryKeyframe 0x0C3：runs 复用切片。
func DecodeTerritoryKeyframe(body []byte, runs []KeyframeRun) (serverTick uint32, out []KeyframeRun, err error) {
	r := reader{b: body}
	if serverTick, err = mustU32(&r); err != nil {
		return
	}
	n, err := mustU16(&r)
	if err != nil {
		return
	}
	out = runs[:0]
	for i := 0; i < int(n); i++ {
		var run KeyframeRun
		if run.Length, err = mustU16(&r); err != nil {
			return
		}
		if run.Owner, err = mustU8(&r); err != nil {
			return
		}
		out = append(out, run)
	}
	return
}

// ---- 事件（Ch3，botclient 用） ----

func DecodePlayerDied(body []byte) (PlayerDiedMsg, error) {
	r := reader{b: body}
	m, err := PlayerDiedMsg{}, error(nil)
	if m.Victim, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.Killer, err = mustU8(&r); err != nil {
		return m, err
	}
	m.Tick, err = mustU32(&r)
	return m, err
}

func DecodePlayerRespawn(body []byte) (PlayerRespawnMsg, error) {
	r := reader{b: body}
	m := PlayerRespawnMsg{}
	var err error
	if m.PlayerID, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.PosX, err = mustU16(&r); err != nil {
		return m, err
	}
	if m.PosY, err = mustU16(&r); err != nil {
		return m, err
	}
	m.Tick, err = mustU32(&r)
	return m, err
}

func DecodePalletDown(body []byte) (PalletDownMsg, error) {
	r := reader{b: body}
	m := PalletDownMsg{}
	var err error
	if m.PalletID, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.ByPlayer, err = mustU8(&r); err != nil {
		return m, err
	}
	m.Tick, err = mustU32(&r)
	return m, err
}

func DecodeDropSpawn(body []byte) (DropSpawnMsg, error) {
	r := reader{b: body}
	m := DropSpawnMsg{}
	var err error
	if m.DropID, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.Type, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.PosX, err = mustU16(&r); err != nil {
		return m, err
	}
	m.PosY, err = mustU16(&r)
	return m, err
}

func DecodeDropTaken(body []byte) (DropTakenMsg, error) {
	r := reader{b: body}
	m := DropTakenMsg{}
	var err error
	if m.DropID, err = mustU8(&r); err != nil {
		return m, err
	}
	m.PlayerID, err = mustU8(&r)
	return m, err
}

func DecodeVaultStart(body []byte) (VaultStartMsg, error) {
	r := reader{b: body}
	m := VaultStartMsg{}
	var err error
	if m.PlayerID, err = mustU8(&r); err != nil {
		return m, err
	}
	if m.VaultID, err = mustU8(&r); err != nil {
		return m, err
	}
	m.DurationTicks, err = mustU8(&r)
	return m, err
}

func DecodeVaultEnd(body []byte) (VaultEndMsg, error) {
	m, err := VaultEndMsg{}, error(nil)
	r := reader{b: body}
	m.PlayerID, err = mustU8(&r)
	return m, err
}

// ---- 便捷错误化读取 ----

func mustU8(r *reader) (uint8, error) {
	v, ok := r.u8()
	if !ok {
		return 0, ErrShort
	}
	return v, nil
}

func mustU16(r *reader) (uint16, error) {
	v, ok := r.u16()
	if !ok {
		return 0, ErrShort
	}
	return v, nil
}

func mustU32(r *reader) (uint32, error) {
	v, ok := r.u32()
	if !ok {
		return 0, ErrShort
	}
	return v, nil
}

func mustI8(r *reader) (int8, error) {
	v, ok := r.i8()
	if !ok {
		return 0, ErrShort
	}
	return v, nil
}
