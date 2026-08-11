package adapter

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestLegacyInputCodec(t *testing.T) {
	// 构造旧客户端 30B 输入:3 帧 + lastSnap + lastTerr。
	payload := make([]byte, 30)
	binary.LittleEndian.PutUint32(payload[0:4], 12345) // clientTick
	binary.LittleEndian.PutUint16(payload[4:6], 300)   // inputSeq
	payload[6] = 3                                     // frameCount
	for i := 0; i < 3; i++ {
		off := 7 + i*5
		payload[off] = byte(10 + i)   // moveX
		payload[off+1] = byte(int8(-20 + i)) // moveY
		binary.LittleEndian.PutUint16(payload[off+2:off+4], uint16(1000+i))
		payload[off+4] = byte(i) // buttons
	}
	binary.LittleEndian.PutUint32(payload[22:26], 888) // lastRecvSnapshotTick
	binary.LittleEndian.PutUint32(payload[26:30], 777) // lastRecvTerritoryTick

	clientTick, seq, lastSnap, user, err := (LegacyInputCodec{}).Decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if clientTick != 12345 || seq != 300 || lastSnap != 888 {
		t.Fatalf("head mismatch: %d %d %d", clientTick, seq, lastSnap)
	}
	want := []byte{3,
		10, 0xEC, 0xE8, 0x03, 0,
		11, 0xED, 0xE9, 0x03, 1,
		12, 0xEE, 0xEA, 0x03, 2,
		0x09, 0x03, 0x00, 0x00, // lastTerr 777 little-endian
	}
	if !bytes.Equal(user, want) {
		t.Fatalf("user data mismatch:\n got %x\nwant %x", user, want)
	}
}

func TestLegacyInputCodecShort(t *testing.T) {
	if _, _, _, _, err := (LegacyInputCodec{}).Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("short payload should error")
	}
}
