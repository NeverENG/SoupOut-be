package room_test

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"soupout-server/internal/lobby"
	"soupout-server/internal/proto"

	"github.com/NeverENG/BanNet/soup-sdk-go"
)

// 集成测试：扮演引擎（T0002M04 UDS 帧协议）连 SDK，
// 4 个 SessionOpen → 开局 MatchStart + keyframe → 输入 → 快照/地盘增量帧。

type fakeEngine struct {
	t    *testing.T
	conn net.Conn
	buf  []byte
}

func (e *fakeEngine) write(typ byte, body []byte) {
	e.t.Helper()
	if err := soup.WriteFrame(e.conn, typ, body); err != nil {
		e.t.Fatal(err)
	}
}

// readUntil 读帧直到满足 match（跳过其它帧）。
func (e *fakeEngine) readUntil(what string, match func(typ byte, body []byte) bool) (byte, []byte) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = e.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		typ, body, err := soup.ReadFrame(e.conn, e.buf)
		if err != nil {
			continue
		}
		if match(typ, body) {
			return typ, body
		}
	}
	e.t.Fatalf("readUntil(%s) 超时", what)
	return 0, nil
}

// readMulticastBody 读一条广播帧（Multicast 或逐玩家 Send 的首帧），返回 payload。
func (e *fakeEngine) readMulticastPayload(msgID uint16) []byte {
	_, body := e.readUntil("msg="+itoa16(msgID), func(typ byte, body []byte) bool {
		switch typ {
		case soup.FrameMulticast:
			if len(body) < 3 {
				return false
			}
			n := int(body[0])
			off := 1 + n*8
			return len(body) >= off+3 && binary.LittleEndian.Uint16(body[off+1:off+3]) == msgID
		case soup.FrameSend:
			return len(body) >= 11 && binary.LittleEndian.Uint16(body[8:10]) == msgID
		}
		return false
	})
	switch typ := body[0]; typ {
	case soup.FrameSend:
		return body[11:] // sess u64 · ch u8 · msg u16 · payload
	default:
		n := int(body[0])
		return body[1+n*8+3:]
	}
}

func itoa16(v uint16) string {
	return string(rune('0'+v/1000)) + string(rune('0'+(v/100)%10)) + string(rune('0'+(v/10)%10)) + string(rune('0'+v%10))
}

func startServer(t *testing.T) (path string, cancel context.CancelFunc) {
	t.Helper()
	// macOS UDS 路径 ≤ 104B（SUN_LEN），t.TempDir 过长；用 /tmp 短名。
	path = filepath.Join(os.TempDir(), "soup-it.sock")
	_ = os.Remove(path)

	ctx, cancel := context.WithCancel(context.Background())
	srv := soup.NewServer(
		soup.WithEngineSocket(path),
		soup.WithTickHz(20),
		soup.WithSnapshotHz(10),
		soup.WithGatekeeper(&lobby.Gatekeeper{}),
	)
	go func() { _ = srv.Run(ctx) }()

	return path, cancel
}

func openSession(e *fakeEngine, sess uint64, token string) {
	body := make([]byte, 8+18+2+len(token))
	binary.LittleEndian.PutUint64(body[0:8], sess)
	copy(body[8:8+4], []byte{127, 0, 0, 1})
	binary.LittleEndian.PutUint16(body[26:28], uint16(len(token)))
	copy(body[28:], token)
	e.write(soup.FrameSessionOpen, body)
}

// sendInput 构造 ch=1 输入帧：SDK 头 8B（clientTick · inputSeq · lastRecvSnapshotTick）
// + user data 20B（frameCount · 3 帧 · lastRecvTerritoryTick）。
// sendInputErr 与 sendInput 相同但返回错误（goroutine 用，连接关闭时退出）。
func sendInputErr(e *fakeEngine, sess uint64, clientTick uint32, seq uint16, moveX, moveY int8, buttons uint8, terrAck uint32) error {
	// FrameDataUp body：sess u64 · ch u8 · msg u16 · payload（SDK 头 8B + user data 20B）。
	body := make([]byte, 8+1+2+8+20)
	binary.LittleEndian.PutUint64(body[0:8], sess)
	body[8] = uint8(soup.ChInput)
	binary.LittleEndian.PutUint16(body[9:11], proto.MsgPlayerInput)
	// payload：SDK 输入头 8B（clientTick · inputSeq · lastRecvSnapshotTick）
	binary.LittleEndian.PutUint32(body[11:15], clientTick)
	binary.LittleEndian.PutUint16(body[15:17], seq)
	binary.LittleEndian.PutUint16(body[17:19], 0) // lastRecvSnapshotTick
	// user data：frameCount u8 · 3×(moveX · moveY · aim · buttons) · lastRecvTerritoryTick u32
	body[19] = 3
	for i := 0; i < 3; i++ {
		off := 20 + i*5
		body[off] = byte(moveX)
		body[off+1] = byte(moveY)
		binary.LittleEndian.PutUint16(body[off+2:off+4], 0) // aim
		body[off+4] = buttons
	}
	binary.LittleEndian.PutUint32(body[35:39], terrAck) // lastRecvTerritoryTick
	return soup.WriteFrame(e.conn, soup.FrameDataUp, body)
}

func TestFullMatchFlow(t *testing.T) {
	path, cancel := startServer(t)
	defer cancel()

	// SDK 单 accept 模型：轮询拨号直到成功；成功即保留该连接作为主连接
	//（失败关闭的探测连接会被 SDK 视作断线并重新 accept）。
	var conn net.Conn
	dialDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(dialDeadline) {
		if c, err := net.Dial("unix", path); err == nil {
			conn = c
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("SDK UDS 3s 未就绪")
	}
	defer conn.Close()
	e := &fakeEngine{t: t, conn: conn, buf: make([]byte, 64*1024)}

	// 1. 握手。
	hello := make([]byte, 6)
	binary.LittleEndian.PutUint16(hello[0:2], 1)
	e.write(soup.FrameEngineHello, hello)
	e.readUntil("LogicHello", func(typ byte, body []byte) bool { return typ == soup.FrameLogicHello })

	// 2. 4 会话 → 开局。
	for i := uint64(0); i < 4; i++ {
		openSession(e, 100+i, "tok")
	}

	// 3. MatchStart 广播（Ch2 0x040）。
	body := e.readMulticastPayload(proto.MsgMatchStart)
	if len(body) < 1+4+4+1+1+1 {
		t.Fatalf("MatchStart too short: %d", len(body))
	}
	off := 0
	mapID := binary.LittleEndian.Uint16(body[off:])
	off += 2
	startTick := binary.LittleEndian.Uint32(body[off:])
	off += 4
	stewTicks := binary.LittleEndian.Uint32(body[off:])
	off += 4
	gridW, gridH := body[off], body[off+1]
	off += 2
	n := int(body[off])
	off++
	if mapID != 0 || startTick != 0 || stewTicks != 4800 || gridW != 96 || gridH != 96 || n != 4 {
		t.Fatalf("MatchStart head mismatch: map=%d start=%d stew=%d grid=%dx%d n=%d", mapID, startTick, stewTicks, gridW, gridH, n)
	}
	// per player: playerId u8 · ingredientId u8 · nickname[16] · spawnX u16 · spawnY u16
	for i := 0; i < n; i++ {
		pid := body[off]
		off += 2 + proto.NicknameLen
		sx := binary.LittleEndian.Uint16(body[off:])
		sy := binary.LittleEndian.Uint16(body[off+2:])
		off += 4
		if pid != uint8(i+1) || sx != 2450 && sx != 7250 || sy != 2450 && sy != 7250 {
			t.Fatalf("MatchStart player %d mismatch: pid=%d spawn=(%d,%d)", i, pid, sx, sy)
		}
	}

	// 4. 开局 keyframe 0x0C3：serverTick u32 · runCount u16 · runs。
	kf := e.readMulticastPayload(proto.MsgTerritoryKeyframe)
	if len(kf) < 6 {
		t.Fatalf("keyframe too short")
	}
	runCount := binary.LittleEndian.Uint16(kf[4:6])
	if runCount < 20 {
		t.Fatalf("keyframe runCount=%d 太小", runCount)
	}
	// runs 解码自检：全图 9216 格必须覆盖。
	total := 0
	p := 6
	for i := 0; i < int(runCount); i++ {
		if p+3 > len(kf) {
			t.Fatalf("keyframe truncated at run %d", i)
		}
		ln := binary.LittleEndian.Uint16(kf[p:])
		total += int(ln)
		p += 3
	}
	if total != 96*96 {
		t.Fatalf("keyframe covers %d cells, want %d", total, 96*96)
	}

	// 5. 4 玩家持续输入（P1 充能扩张，其余移动）；地盘 ACK 持续回传
	// 最新收到的 keyframe serverTick（真实客户端行为；首帧 keyframe 为 tick=0）。
	var ackTick atomic.Uint32
	go func() {
		for tick := uint32(1); tick <= 120; tick++ {
			a := ackTick.Load()
			if sendInputErr(e, 100, tick, uint16(tick), 0, 0, proto.BtnExpand, a) != nil ||
				sendInputErr(e, 101, tick, uint16(tick), 30, 30, 0, a) != nil ||
				sendInputErr(e, 102, tick, uint16(tick), -30, 30, 0, a) != nil ||
				sendInputErr(e, 103, tick, uint16(tick), 30, -30, 0, a) != nil {
				return // 测试结束连接关闭
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	// 6. 收快照（msg=0，10Hz）与地盘增量 0x0C1 / 比分 0x0C2。
	snapOK, deltaOK, scoreOK := false, false, false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !(snapOK && deltaOK && scoreOK) {
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		typ, body, err := soup.ReadFrame(e.conn, e.buf)
		if err != nil {
			continue
		}
		switch typ {
		case soup.FrameMulticast:
			// Multicast：n u8 · sess[n] u64 · ch u8 · msg u16 · payload（payload 起点 off）
			n := int(body[0])
			off := 1 + n*8 + 3
			if len(body) >= off+6 {
				switch binary.LittleEndian.Uint16(body[off-2:off]) {
				case proto.MsgTerritoryKeyframe:
					ackTick.Store(binary.LittleEndian.Uint32(body[off:off+4]))
				case proto.MsgScoreTick:
					scoreOK = true
				}
			}
		case soup.FrameSend:
			if len(body) < 11 {
				continue
			}
			msg := binary.LittleEndian.Uint16(body[9:11])
			payload := body[11:]
			switch msg {
			case 0: // 快照（SDK 保留号）：6B SDK 头 + body
				if len(payload) >= 6+1 && payload[6] > 0 {
					snapOK = true
				}
			case proto.MsgTerritoryDelta:
				deltaOK = true
			case proto.MsgScoreTick:
				scoreOK = true
			}
		}
	}
	if !snapOK {
		t.Fatal("8s 内未收到快照")
	}
	if !deltaOK {
		t.Fatal("8s 内未收到地盘增量 0x0C1")
	}
	if !scoreOK {
		t.Fatal("8s 内未收到比分 0x0C2")
	}
}
