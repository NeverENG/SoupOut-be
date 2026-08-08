package territory

// RLEWriter 是 EncodeRLE 的写入目标。
// 与 T0004M03F01 的偏差：原签名用 *proto.Buffer，但依赖图禁止 territory
// import proto（T0004M02）。由 room 层用 proto.Buffer 实现本接口对接。
type RLEWriter interface {
	PutU16(v uint16)
	PutU8(v uint8)
}

// CountRLE 返回全图行优先扫描的 run 数（供 0x0C3 头部 runCount）。
func (f *Field) CountRLE() int {
	runOwner := uint8(0xFF)
	n := 0
	for y := 0; y < f.h; y++ {
		base := y * f.w
		for x := 0; x < f.w; x++ {
			o := f.owner[base+x]
			if o != runOwner {
				runOwner = o
				n++
			}
		}
	}
	return n
}

// EncodeRLE 输出全量关键帧：行优先扫描的游程编码
// （T0001M02F05 `0x0C3`：[per run] length u16 · owner u8，不含 serverTick）。
// 全图 9216 格连续 run 长度 ≤ 9216 < 65535，不会溢出 u16。
func (f *Field) EncodeRLE(w RLEWriter) {
	runOwner := uint8(0xFF) // 哨兵：无进行中的 run
	runLen := 0
	for y := 0; y < f.h; y++ {
		base := y * f.w
		for x := 0; x < f.w; x++ {
			o := f.owner[base+x]
			if o == runOwner {
				runLen++
				continue
			}
			if runOwner != 0xFF {
				w.PutU16(uint16(runLen))
				w.PutU8(runOwner)
			}
			runOwner = o
			runLen = 1
		}
	}
	if runOwner != 0xFF {
		w.PutU16(uint16(runLen))
		w.PutU8(runOwner)
	}
}
