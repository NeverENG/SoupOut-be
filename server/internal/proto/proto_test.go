package proto

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// tb 是测试用 Writer：与 BufferWriter 等价的裸字节实现，可读回 payload。
type tb struct{ b []byte }

func (w *tb) PutU8(v uint8)     { w.b = append(w.b, v) }
func (w *tb) PutU16(v uint16)   { w.b = append(w.b, byte(v), byte(v>>8)) }
func (w *tb) PutU32(v uint32)   { w.b = append(w.b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
func (w *tb) PutI16(v int16)    { w.PutU16(uint16(v)) }
func (w *tb) PutVarint(v int64) { w.b = appendVarint(w.b, v) }
func (w *tb) PutRaw(p []byte)   { w.b = append(w.b, p...) }

func appendVarint(b []byte, v int64) []byte {
	u := uint64(v)<<1 ^ uint64(v>>63)
	for u >= 0x80 {
		b = append(b, byte(u)|0x80)
		u >>= 7
	}
	return append(b, byte(u))
}

func appendU16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }
func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// TestDecodePlayerInput 0x080 输入 user data 解码（SDK 剥离 8B 头后 20B；布局 + 截断 + frameCount 裁剪）。
func TestDecodePlayerInput(t *testing.T) {
	// 20B user data：frameCount u8 · 3×{moveX i8 · moveY i8 · aimAngle u16 · buttons u8} · lastRecvTerritoryTick u32
	b := make([]byte, 20)
	b[0] = 3
	b[1] = 64   // 帧0 moveX
	my := int8(-32)
	b[2] = byte(my) // 帧0 moveY
	binary.LittleEndian.PutUint16(b[3:5], 0x8000)
	b[5] = BtnExpand
	binary.LittleEndian.PutUint32(b[16:20], 98) // lastRecvTerritoryTick

	m, err := DecodeInputUserData(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.FrameCount != 3 {
		t.Fatalf("FrameCount = %d, want 3", m.FrameCount)
	}
	if m.Frames[0].MoveX != 64 || m.Frames[0].MoveY != -32 || m.Frames[0].AimAngle != 0x8000 {
		t.Fatalf("frame0 mismatch: %+v", m.Frames[0])
	}
	if m.Frames[0].Buttons&BtnExpand == 0 || m.LastRecvTerritoryTick != 98 {
		t.Fatalf("expand/ack mismatch: %+v", m)
	}
	// 截断不 panic
	for cut := 0; cut < len(b); cut++ {
		if _, err := DecodeInputUserData(b[:cut]); err == nil {
			t.Fatalf("cut %d should error", cut)
		}
	}
	// frameCount 超限裁剪（声明 4 帧但 body 只够 3）
	b2 := append([]byte{4}, b[1:]...)
	if m2, err := DecodeInputUserData(b2); err != nil || m2.FrameCount != 3 {
		t.Fatalf("frameCount clamp: %+v err=%v", m2, err)
	}
}
func TestEncodeSnapshotLayout(t *testing.T) {
	states := []PlayerState{
		{PlayerID: 1, PosX: 100, PosY: 200, VelX: 5, VelY: -3, AimAngle: 65535, Mass: 1000, StateFlags: FlagCharging, HP: 80},
		{PlayerID: 2, PosX: 300, PosY: 400, Mass: 1200, HP: 100},
	}
	var w tb
	EncodeSnapshotBody(&w, states)

	// 手写期望字节
	want := []byte{}
	want = append(want, 2)
	for _, s := range states {
		want = append(want, s.PlayerID)
		want = appendU16(want, s.PosX)
		want = appendU16(want, s.PosY)
		want = append(want, byte(s.VelX), byte(s.VelY))
		want = appendU16(want, s.AimAngle)
		want = appendU16(want, s.Mass)
		want = append(want, s.StateFlags, s.HP)
	}
	if !bytes.Equal(w.b, want) {
		t.Fatalf("snapshot bytes mismatch:\n got %x\nwant %x", w.b, want)
	}
	// 解码往返
	out, err := DecodeSnapshotBody(w.b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Mass != 1000 || out[0].VelY != -3 || out[1].PlayerID != 2 {
		t.Fatalf("decode mismatch: %+v", out)
	}
	// 复用切片
	if out2, err := DecodeSnapshotBody(w.b, out); err != nil || len(out2) != 2 {
		t.Fatalf("reuse: %v", err)
	}
}

func TestChannelOf(t *testing.T) {
	cases := []struct {
		msg uint16
		ch  soup.Channel
	}{
		{MsgCreateRoom, soup.ChReliableOrdered},
		{MsgMatchStart, soup.ChReliableOrdered},
		{MsgPlayerInput, soup.ChInput},
		{MsgSnapshot, soup.ChUnreliable},
		{MsgTerritoryKeyframe, soup.ChReliableOrdered},
		{MsgPlayerDied, soup.ChReliableUnordered},
		{MsgScoreTick, soup.ChUnreliable},
	}
	for _, c := range cases {
		if got := ChannelOf(c.msg); got != c.ch {
			t.Errorf("ChannelOf(0x%03X) = %d, want %d", c.msg, got, c.ch)
		}
	}
}

func TestTerritoryDeltaVarint(t *testing.T) {
	var w tb
	EncodeTerritoryDelta(&w, 5, 3, []TerritoryGroup{
		{Owner: 1, Cells: []uint32{100, 105, 305, 306}},
		{Owner: 0, Cells: []uint32{0}},
	})
	// 手写期望
	want := []byte{}
	want = appendU32(want, 5)
	want = appendU32(want, 3)
	want = append(want, 2)
	want = append(want, 1)
	want = appendU16(want, 4)
	for _, d := range []int64{100, 5, 200, 1} {
		want = appendVarint(want, d)
	}
	want = append(want, 0)
	want = appendU16(want, 1)
	want = appendVarint(want, 0)
	if !bytes.Equal(w.b, want) {
		t.Fatalf("delta mismatch:\n got %x\nwant %x", w.b, want)
	}

	st, since, groups, err := DecodeTerritoryDelta(w.b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st != 5 || since != 3 || len(groups) != 2 {
		t.Fatalf("head mismatch: %d %d %d", st, since, len(groups))
	}
	wantCells := []uint32{100, 105, 305, 306}
	for i, c := range wantCells {
		if groups[0].Cells[i] != c {
			t.Fatalf("cell[%d] = %d, want %d", i, c, groups[0].Cells[i])
		}
	}
	if groups[1].Owner != 0 || groups[1].Cells[0] != 0 {
		t.Fatalf("group1: %+v", groups[1])
	}
	// 复用：第二次解码结果一致（Cells 每次分配，botclient 低频场景可接受）
	if _, _, g2, err := DecodeTerritoryDelta(w.b, groups); err != nil ||
		len(g2) != 2 || g2[0].Cells[3] != 306 {
		t.Fatalf("second decode broken: %v", err)
	}
	// 截断
	if _, _, _, err := DecodeTerritoryDelta(w.b[:10], nil); err == nil {
		t.Fatal("truncated delta should error")
	}
}

func TestKeyframeAndEvents(t *testing.T) {
	// 0x0C3 关键帧（run 主体由 territory.EncodeRLE 提供，这里只测解码）
	b := []byte{}
	b = appendU32(b, 10)
	b = appendU16(b, 3)
	b = appendU16(b, 100)
	b = append(b, 1)
	b = appendU16(b, 50)
	b = append(b, 0)
	b = appendU16(b, 9066)
	b = append(b, 15)
	st, runs, err := DecodeTerritoryKeyframe(b, nil)
	if err != nil || st != 10 || len(runs) != 3 || runs[2].Length != 9066 || runs[2].Owner != 15 {
		t.Fatalf("keyframe: %d %+v %v", st, runs, err)
	}

	// 事件编码→解码往返
	type eventCase struct {
		name string
		enc  func(w Writer)
		dec  func([]byte) (any, error)
	}
	cases := []eventCase{
		{"died", func(w Writer) { EncodePlayerDied(w, PlayerDiedMsg{Victim: 3, Killer: 1, Tick: 77}) },
			func(b []byte) (any, error) { return DecodePlayerDied(b) }},
		{"respawn", func(w Writer) { EncodePlayerRespawn(w, PlayerRespawnMsg{PlayerID: 2, PosX: 100, PosY: 200, Tick: 88}) },
			func(b []byte) (any, error) { return DecodePlayerRespawn(b) }},
		{"pallet", func(w Writer) { EncodePalletDown(w, PalletDownMsg{PalletID: 5, ByPlayer: 4, Tick: 99}) },
			func(b []byte) (any, error) { return DecodePalletDown(b) }},
		{"dropspawn", func(w Writer) { EncodeDropSpawn(w, DropSpawnMsg{DropID: 1, Type: 2, PosX: 10, PosY: 20}) },
			func(b []byte) (any, error) { return DecodeDropSpawn(b) }},
		{"droptaken", func(w Writer) { EncodeDropTaken(w, DropTakenMsg{DropID: 1, PlayerID: 3}) },
			func(b []byte) (any, error) { return DecodeDropTaken(b) }},
		{"vaultstart", func(w Writer) { EncodeVaultStart(w, VaultStartMsg{PlayerID: 1, VaultID: 2, DurationTicks: 20}) },
			func(b []byte) (any, error) { return DecodeVaultStart(b) }},
		{"vaultend", func(w Writer) { EncodeVaultEnd(w, VaultEndMsg{PlayerID: 4}) },
			func(b []byte) (any, error) { return DecodeVaultEnd(b) }},
	}
	for _, c := range cases {
		var w tb
		c.enc(&w)
		got, err := c.dec(w.b)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got == nil {
			t.Fatalf("%s: nil", c.name)
		}
	}
	// 截断不 panic
	if _, err := DecodePlayerDied([]byte{1}); err == nil {
		t.Fatal("short died should error")
	}
}

func TestScoreTick(t *testing.T) {
	var w tb
	ratios := [4]uint16{1000, 2000, 3000, 4000}
	EncodeScoreTick(&w, 123, ratios)
	st, got, err := DecodeScoreTick(w.b)
	if err != nil || st != 123 || got != ratios {
		t.Fatalf("score: %d %v %v", st, got, err)
	}
}

func TestLobbyMessages(t *testing.T) {
	// RoomCreated / JoinResult / RoomState 布局
	var code [RoomCodeLen]byte
	copy(code[:], "ABCD")
	var w tb
	EncodeRoomCreated(&w, code, 3)
	want := []byte{'A', 'B', 'C', 'D', 3}
	if !bytes.Equal(w.b, want) {
		t.Fatalf("roomcreated: %x", w.b)
	}

	w = tb{}
	EncodeJoinResult(&w, JoinFull, 2)
	if !bytes.Equal(w.b, []byte{2, 2}) {
		t.Fatalf("joinresult: %x", w.b)
	}

	var nick [NicknameLen]byte
	copy(nick[:], "ponger")
	w = tb{}
	EncodeRoomState(&w, code, []RoomPlayer{{PlayerID: 1, Nickname: nick, IngredientID: 2, Ready: 1}})
	if len(w.b) != RoomCodeLen+1+1+NicknameLen+1+1 {
		t.Fatalf("roomstate len = %d", len(w.b))
	}
	// 解码 C→S 大厅
	if n, err := DecodeNickname(append([]byte(nil), nick[:]...)); err != nil || string(n[:6]) != "ponger" {
		t.Fatalf("nickname: %q %v", n[:6], err)
	}
	rc, n2, err := DecodeRoomCodeAndNickname(append(append([]byte(nil), code[:]...), nick[:]...))
	if err != nil || string(rc[:]) != "ABCD" || string(n2[:6]) != "ponger" {
		t.Fatalf("roomcode+nick: %q %q %v", rc[:], n2[:6], err)
	}
	if v, err := DecodeIngredient([]byte{7}); err != nil || v != 7 {
		t.Fatalf("ingredient: %d %v", v, err)
	}
	if v, err := DecodeReady([]byte{1}); err != nil || v != 1 {
		t.Fatalf("ready: %d %v", v, err)
	}
}

func TestVarintRoundTrip(t *testing.T) {
	vals := []int64{0, 1, -1, 100, -100, 1 << 20, -(1 << 20), 306}
	for _, v := range vals {
		enc := appendVarint(nil, v)
		r := reader{b: enc}
		got, ok := r.varint()
		if !ok || got != v {
			t.Fatalf("varint(%d): got %d ok=%v", v, got, ok)
		}
	}
}
