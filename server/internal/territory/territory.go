// Package territory 实现 96×96 连续区域场：增量测地距离膨胀（Incremental
// Geodesic Dilation）、边界对抗、流失、面积统计与增量导出。
//
// 规格：T0004M03F01（实现架构）· T0001M03（同步方案）· D0001M05（数值）。
// 硬约束（BE0000M05）：全整数 Chamfer 距离（正交 +8 / 对角 +11）、堆元素打包
// uint32 = dist<<16|cell、DiffSince 用环形日志 + bitset 去重、热路径零分配。
// 本包只依赖 pkg/fixed，可独立测试与可视化。
package territory

import "soupout-server/pkg/fixed"

// Cell 是网格单元：y*w + x，值域 0..9215（96×96）。
type Cell uint16

// 格归属（T0001M03F01：u4，0=原汤 1..4=玩家 15=锅外）。
const (
	OwnerSoup uint8 = 0
	OwnerWall uint8 = 15

	// MaxPlayers 是玩家数。
	MaxPlayers = 4
)

// Chamfer 步长（T0004M03F01）。
const (
	stepOrtho = 8  // 正交邻居
	stepDiag  = 11 // 对角邻居（≈ 8√2）
)

// KNOB_steal_penalty（D0001M05）：抢占敌方地盘的额外距离代价。
const StealPenalty = 8

// Circle 描述汤锅：内切圆，半径 R（格单位），圆心 = 网格中心
// (w/2 - 0.5, h/2 - 0.5) 格坐标（世界中心 24,24 单位 = 格 47.5,47.5）。
type Circle struct{ R int }

// Field 是 96×96 地盘场。
type Field struct {
	w, h int
	n    int

	owner []uint8  // 9216：0=原汤 1..4=玩家 15=锅外
	dist  []uint16 // 9216：该格到其所有者源的测地距离（Chamfer 单位，供可视化/调试）

	inF      [MaxPlayers][]uint64 // frontier 成员 bitset（push 去重用）
	frontier [MaxPlayers]minHeap  // 每玩家扩张前沿
	claimed  [MaxPlayers]maxHeap  // 每玩家已占格按 dist 排序（流失时从最外层退）
	blocked  [MaxPlayers][]uint32 // 本 tick 被僵持挡住的格（打包值，下 tick 重入 frontier）

	R    [MaxPlayers]fixed.F // 每玩家扩张半径（Chamfer 单位，Q22.10）
	area [MaxPlayers + 1]int32

	log       changeRing
	diffStamp []uint64 // DiffSince 去重时间戳（epoch 技巧，免清零）
	diffEpoch uint64
	tick      uint32
}

func pack(d uint16, c Cell) uint32 { return uint32(d)<<16 | uint32(c) }
func packDist(v uint32) uint16     { return uint16(v >> 16) }
func packCell(v uint32) Cell       { return Cell(v & 0xFFFF) }

// New 创建 w×h 网格。锅外格标 OwnerWall，锅内标 OwnerSoup。
// 锅内判定：格中心到锅心 (w/2-0.5, h/2-0.5) 的距离 ≤ R 格（整数化：4 倍缩放）。
func New(w, h int, pot Circle) *Field {
	f := &Field{w: w, h: h, n: w * h}
	f.owner = make([]uint8, f.n)
	f.dist = make([]uint16, f.n)
	f.diffStamp = make([]uint64, f.n)
	bits := (f.n + 63) / 64
	for p := 0; p < MaxPlayers; p++ {
		f.inF[p] = make([]uint64, bits)
		// 容量分析：
		//  frontier：单玩家地盘外圈 ≤ 锅周长 ~240 格；frontier = 外圈 + blocked 重入 ≤ 480 → 2048 余量 4×。
		//  blocked：与敌方前沿接触的格数 ≤ 外圈 ~240 → 2048 余量 8×。
		//  claimed：每格每次易主在持有方堆里留一条 stale 条目（被抢时不清堆）。
		//    单局假设（T0001M06F03：约 4 分钟 / 3600~4800 tick）：一次全占 7238 条
		//    + 边界 ~200 格拉锯易主 ~50 次 ≈ 10000 条 → 容量 2×f.n 覆盖。
		//    超限 panic 暴露容量分析失效（比静默扩容 → 破坏 T7 零分配好）。
		f.frontier[p].init(2048)
		f.claimed[p].init(2 * f.n)
		f.blocked[p] = make([]uint32, 0, 2048)
	}
	f.log.init(4096)

	// 锅心 2 倍格坐标：圆心 (w/2-0.5, h/2-0.5) ⇒ 2cx = w-1, 2cy = h-1；半径 2R。
	twoCX, twoCY := w-1, h-1
	twoR := 2 * pot.R
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := 2*x - twoCX
			dy := 2*y - twoCY
			if dx*dx+dy*dy <= twoR*twoR {
				f.owner[y*w+x] = OwnerSoup
				f.area[0]++
			} else {
				f.owner[y*w+x] = OwnerWall
			}
		}
	}
	return f
}

// Seed 给玩家 owner（1..4）在 center 播下 targetArea 格的地盘。
// 用 Chamfer Dijkstra（minHeap 按 dist 优先）从 center 生长，保证 dist 单调、
// 形状为测地圆盘；随后初始化该玩家的 frontier 与 claimed 堆。
// targetArea 超过锅内剩余格时 clamp。建房时调用，允许分配。
func (f *Field) Seed(owner uint8, center Cell, targetArea int) {
	if owner < 1 || owner > MaxPlayers {
		panic("territory: Seed owner out of range")
	}
	if f.owner[center] != OwnerSoup {
		panic("territory: Seed center not on soup")
	}
	idx := int(owner - 1)
	if targetArea > int(f.area[0]) {
		targetArea = int(f.area[0])
	}

	// Chamfer Dijkstra 生长
	var h minHeap
	h.init(targetArea*2 + 64)
	h.push(pack(0, center))
	f.claimRaw(owner, center, 0)
	claimed := 1
	var nb [8]nbr
	for h.len() > 0 && claimed < targetArea {
		v := h.pop()
		c, d := packCell(v), packDist(v)
		n := f.neighbors8(c, &nb)
		for i := 0; i < n && claimed < targetArea; i++ {
			b := nb[i]
			if f.owner[b.c] != OwnerSoup {
				continue
			}
			nd := uint16(int(d) + b.cost)
			f.claimRaw(owner, b.c, nd)
			h.push(pack(nd, b.c))
			claimed++
		}
	}

	// 初始化 frontier：己方地盘边界外的可达邻格
	for c := 0; c < f.n; c++ {
		if f.owner[c] != owner {
			continue
		}
		n := f.neighbors8(Cell(c), &nb)
		for i := 0; i < n; i++ {
			b := nb[i]
			o := f.owner[b.c]
			if o == owner || o == OwnerWall {
				continue
			}
			if !f.inSet(idx, b.c) {
				f.frontier[idx].push(pack(uint16(int(f.dist[c])+b.cost), b.c))
				f.inSetSet(idx, b.c)
			}
		}
	}
	// 初始化 claimed 堆：全部己方格按 dist 排序；顺带记录 maxDist 作为 R 初始值
	maxD := uint16(0)
	for c := 0; c < f.n; c++ {
		if f.owner[c] == owner {
			f.claimed[idx].push(pack(f.dist[c], Cell(c)))
			if f.dist[c] > maxD {
				maxD = f.dist[c]
			}
		}
	}
	f.R[idx] = fixed.I(int32(maxD)) // R 起始 = 种子盘边缘测地距离（Chamfer）
}

// R 增长上限：Q22.10 下 int32 值域 ±2048.999；本作锅半径 48 格 = 384 Chamfer，
// 上限取 8192 Chamfer（8×锅直径），为 dist(uint16) 打包留足余量（8192+19 < 65535）。
// 单局 4 分钟内 R 到 8192 需要 13 万 tick，实际不可达；clamp 只是防溢出不变量。
const rMax = fixed.F(8192) << fixed.FracBits

// Step 推进一个 tick：每玩家（充能中）R += rate，从 frontier 弹出 dist ≤ R 的格
// 执行占领 / 僵持 / 抢占（T0001M03F03/F04）。tick 用于变更日志时间戳。
// 兼容入口：等价于 StepRates（charging[i] → rates[i] = rate）。
func (f *Field) Step(charging [MaxPlayers]bool, rate fixed.F, tick uint32) {
	var rates [MaxPlayers]fixed.F
	for i := 0; i < MaxPlayers; i++ {
		if charging[i] {
			rates[i] = rate
		}
	}
	f.StepRates(rates, tick)
}

// StepRates 逐玩家扩张速率（rates[i] = 0 表示该玩家本 tick 不充能）。
// 支持道具倍率（如润甜 ×2.5）等逐玩家差异化，速率语义与 Step 相同。
func (f *Field) StepRates(rates [MaxPlayers]fixed.F, tick uint32) {
	f.tick = tick
	var nb [8]nbr
	for p := 0; p < MaxPlayers; p++ {
		if rates[p] == 0 {
			continue
		}
		// 1. 上 tick 僵持的格重入 frontier（保留原 dist）
		for _, v := range f.blocked[p] {
			c := packCell(v)
			o := f.owner[c]
			if o == OwnerWall || o == uint8(p+1) {
				continue // 锅外 / 已被自己占：丢弃
			}
			if !f.inSet(p, c) {
				f.frontier[p].push(v)
				f.inSetSet(p, c)
			}
		}
		f.blocked[p] = f.blocked[p][:0]

		// 2. 半径增长（clamp 防溢出：R 必须远小于 uint16(dist) 上限）
		f.R[p] = f.R[p].Add(rates[p])
		if f.R[p] > rMax {
			f.R[p] = rMax
		}

		// 3. 扩张：dist（整数 Chamfer）≤ floor(R)
		rInt := int(f.R[p].ToInt())
		for f.frontier[p].len() > 0 {
			v := f.frontier[p].top()
			if int(packDist(v)) > rInt {
				break
			}
			f.frontier[p].pop()
			c, d := packCell(v), packDist(v)
			f.inSetClear(p, c)
			o := f.owner[c]
			switch {
			case o == OwnerWall, o == uint8(p+1):
				continue // 锅外 / 已是自己的：丢弃过期条目
			case o != OwnerSoup:
				q := int(o - 1)
				if rates[q] != 0 {
					// 僵持：下 tick 重试。blocked 容量 2048，超出即不变量失效。
					if len(f.blocked[p]) == cap(f.blocked[p]) {
						panic("territory: blocked capacity exceeded")
					}
					f.blocked[p] = append(f.blocked[p], v)
					continue
				}
				f.claim(p, c, d, tick) // 敌方未充能：抢占
			default:
				f.claim(p, c, d, tick)
			}
			// 4. 推八邻
			n := f.neighbors8(c, &nb)
			for i := 0; i < n; i++ {
				b := nb[i]
				o2 := f.owner[b.c]
				if o2 == uint8(p+1) || o2 == OwnerWall {
					continue
				}
				if f.inSet(p, b.c) {
					continue
				}
				cost := b.cost
				if o2 != OwnerSoup {
					cost += StealPenalty // 抢地比开荒慢（D0001M05）
				}
				f.frontier[p].push(pack(uint16(int(d)+cost), b.c))
				f.inSetSet(p, b.c)
			}
		}
	}
}

// Dissolve 流失玩家 owner（1..4）的地盘：R 按 rate 下降，从 claimed 堆最大
// dist 端弹格归还原汤（形状不出现空洞，T5）。rate 由 sim 层按 D0001M07
// （自身面积 1.5%/s）换算为 Chamfer 半径衰减量传入。
func (f *Field) Dissolve(owner uint8, rate fixed.F, tick uint32) {
	if owner < 1 || owner > MaxPlayers {
		return
	}
	f.tick = tick
	idx := int(owner - 1)
	f.R[idx] = f.R[idx].Sub(rate)
	if f.R[idx] < 0 {
		f.R[idx] = 0
	}
	rInt := int(f.R[idx].ToInt())
	for f.claimed[idx].len() > 0 {
		v := f.claimed[idx].top()
		if int(packDist(v)) <= rInt {
			break
		}
		f.claimed[idx].pop()
		c := packCell(v)
		if f.owner[c] != owner {
			continue // 已被抢走
		}
		f.owner[c] = OwnerSoup
		f.area[0]++
		f.area[idx+1]--
		f.log.record(Change{Tick: tick, Cell: c, Owner: OwnerSoup})
		// c 回原汤后，邻格（仍属 owner）扩张时自会重新 push 它，无需额外处理。
	}
}

// claim 把格 c 判给玩家（owner 1..4），更新面积、变更日志、claimed 堆，
// 并清除该格在所有玩家 frontier 位图中的标记（O(1)，与变更数成正比）。
func (f *Field) claim(p int, c Cell, d uint16, tick uint32) {
	old := f.owner[c]
	f.owner[c] = uint8(p + 1)
	f.dist[c] = d
	if old == OwnerSoup {
		f.area[0]--
	} else {
		f.area[int(old)]--
	}
	f.area[p+1]++
	f.log.record(Change{Tick: tick, Cell: c, Owner: uint8(p + 1)})
	for q := 0; q < MaxPlayers; q++ {
		f.inSetClear(q, c)
	}
	f.claimed[p].push(pack(d, c))
}

// claimRaw 是 Seed 专用：只写 owner/dist/area，不进变更日志、不动堆。
func (f *Field) claimRaw(owner uint8, c Cell, d uint16) {
	f.owner[c] = owner
	f.dist[c] = d
	f.area[0]--
	f.area[int(owner)]++
}

// Ratios 返回各玩家面积万分比（按 playerId 1..4 顺序），基于锅内总格数归一。
func (f *Field) Ratios() [MaxPlayers]uint16 {
	total := 0
	for _, a := range f.area {
		total += int(a)
	}
	var r [MaxPlayers]uint16
	if total <= 0 {
		return r
	}
	for p := 0; p < MaxPlayers; p++ {
		r[p] = uint16(int64(f.area[p+1]) * 10000 / int64(total))
	}
	return r
}

// Area 返回玩家 owner（1..4）当前格数；owner 为 0 时返回原汤格数。
func (f *Field) Area(owner uint8) int32 {
	if owner > MaxPlayers {
		return 0
	}
	return f.area[int(owner)]
}

// OwnerAt 返回格 c 的归属。
func (f *Field) OwnerAt(c Cell) uint8 { return f.owner[c] }

// Tick 返回最近一次 Step/Dissolve 的 tick。
func (f *Field) Tick() uint32 { return f.tick }

// Hash 确定性哈希（内联 FNV-1a 64，零分配），用于回放校验（T6）。
func (f *Field) Hash() uint64 {
	h := uint64(14695981039346656037)
	for _, o := range f.owner {
		h ^= uint64(o)
		h *= 1099511628211
	}
	for p := 0; p < MaxPlayers; p++ {
		h ^= uint64(f.R[p])
		h *= 1099511628211
		h ^= uint64(f.area[p+1])
		h *= 1099511628211
	}
	h ^= uint64(f.tick)
	h *= 1099511628211
	return h
}

// nbr 是八邻（格 + Chamfer 步长）。
type nbr struct {
	c    Cell
	cost int
}

// neighbors8 填出 c 的八邻（含步长），返回数量。锅外格同样返回，由调用方按 owner 过滤。
func (f *Field) neighbors8(c Cell, out *[8]nbr) int {
	x, y := int(c)%f.w, int(c)/f.w
	n := 0
	if y > 0 {
		out[n] = nbr{Cell(int(c) - f.w), stepOrtho}
		n++
		if x > 0 {
			out[n] = nbr{Cell(int(c) - f.w - 1), stepDiag}
			n++
		}
		if x < f.w-1 {
			out[n] = nbr{Cell(int(c) - f.w + 1), stepDiag}
			n++
		}
	}
	if y < f.h-1 {
		out[n] = nbr{Cell(int(c) + f.w), stepOrtho}
		n++
		if x > 0 {
			out[n] = nbr{Cell(int(c) + f.w - 1), stepDiag}
			n++
		}
		if x < f.w-1 {
			out[n] = nbr{Cell(int(c) + f.w + 1), stepDiag}
			n++
		}
	}
	if x > 0 {
		out[n] = nbr{Cell(int(c) - 1), stepOrtho}
		n++
	}
	if x < f.w-1 {
		out[n] = nbr{Cell(int(c) + 1), stepOrtho}
		n++
	}
	return n
}

func (f *Field) inSet(p int, c Cell) bool {
	return f.inF[p][c>>6]&(1<<(c&63)) != 0
}

func (f *Field) inSetSet(p int, c Cell) {
	f.inF[p][c>>6] |= 1 << (c & 63)
}

func (f *Field) inSetClear(p int, c Cell) {
	f.inF[p][c>>6] &^= 1 << (c & 63)
}
