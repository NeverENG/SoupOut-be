package territory

// T1–T8 验收测试（T0004M03F01）。
// T7 是唯一对分配敏感的测试：若 Step/DiffSince 内出现任何堆分配，AllocsPerRun 会报告 > 0。

import (
	"bytes"
	"encoding/binary"
	"path/filepath"
	"testing"

	"soupout-server/pkg/fixed"
)

const (
	gridW  = 96
	gridH  = 96
	seed10 = 724 // 10% = 7238 × 10% ≈ 724 格（T0001M03F01）
)

var pot = Circle{R: 48}

// expandRate 是 D0001M05 KNOB_expand_rate = 64（Q22.10 原始值）= 0.0625 Chamfer/tick。
// 注意：不是 fixed.I(64)（那是 64.0）。
var expandRate = fixed.F(64)

func newTestField() *Field { return New(gridW, gridH, pot) }

// seedCorners 四角对称开局（A0001M09 地图坐标表未定，territory 测试自选）。
func seedCorners(f *Field, area int) {
	f.Seed(1, Cell(24*gridW+24), area)
	f.Seed(2, Cell(24*gridW+72), area)
	f.Seed(3, Cell(72*gridW+24), area)
	f.Seed(4, Cell(72*gridW+72), area)
}

func TestT1SeedArea(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	for p := uint8(1); p <= 4; p++ {
		got := f.Area(p)
		// T1：Seed 后各人面积 = 10% ± 1%
		if got < seed10-72 || got > seed10+72 {
			t.Errorf("player %d area = %d, want %d ± 1%%", p, got, seed10)
		}
	}
	// 总面积守恒：area 总和 = 锅内格数
	inPot := 0
	for _, o := range f.owner {
		if o != OwnerWall {
			inPot++
		}
	}
	sum := 0
	for _, a := range f.area {
		sum += int(a)
	}
	if sum != inPot {
		t.Fatalf("area sum = %d, in-pot cells = %d", sum, inPot)
	}
	t.Logf("in-pot cells = %d, ratios = %v", inPot, f.Ratios())
}

func TestT2SmoothExpansion(t *testing.T) {
	f := newTestField()
	f.Seed(1, Cell(48*gridW+48), seed10) // 单人中场
	var charging [4]bool
	charging[0] = true
	prev := f.Area(1)
	var changes []Change
	var maxFlips int
	for tick := uint32(1); tick <= 2000; tick++ {
		f.Step(charging, expandRate, tick)
		cur := f.Area(1)
		if cur < prev {
			t.Fatalf("tick %d: area shrank %d → %d", tick, prev, cur)
		}
		if !f.DiffSince(tick-1, &changes) {
			t.Fatal("DiffSince(tick-1) returned false")
		}
		if n := len(changes); n > maxFlips {
			maxFlips = n
		}
		if len(changes) > 32 {
			t.Fatalf("tick %d: %d flips in one tick > 32", tick, len(changes))
		}
		prev = cur
	}
	if f.Area(1) <= seed10+200 {
		t.Fatalf("no meaningful growth: %d", f.Area(1))
	}
	t.Logf("area = %d (%.1f%%), max flips/tick = %d", f.Area(1), float64(f.Area(1))*100/7238, maxFlips)
}

func TestT3Stalemate(t *testing.T) {
	f := newTestField()
	// 双人相对中场，中间隔原汤；都充能 → 相遇后边界僵持不动（T0001M03F04）
	f.Seed(1, Cell(48*gridW+30), seed10)
	f.Seed(2, Cell(48*gridW+66), seed10)
	var charging [4]bool
	charging[0] = true
	charging[1] = true
	var changes []Change
	const total = 4000
	for tick := uint32(1); tick <= total; tick++ {
		f.Step(charging, expandRate, tick)
	}
	// 相遇断言：中间原汤已吃光（两圆盘半径 15.2+，中心距 36 → 各推进 ~2.8 格即可相遇）
	if f.Area(0) > 7238-2*seed10 {
		t.Fatalf("territories did not meet: soup left = %d", f.Area(0))
	}
	// 后段窗口内不得发生 1↔2 互抢：每格 owner 序列中不得出现两次不同非零归属
	if !f.DiffSince(3500, &changes) {
		t.Fatal("DiffSince(3500) returned false")
	}
	cur := make(map[Cell]uint8)
	steals := 0
	for _, c := range changes {
		if old, ok := cur[c.Cell]; ok && old != OwnerSoup && c.Owner != old && c.Owner != OwnerSoup {
			steals++
		}
		cur[c.Cell] = c.Owner
	}
	if steals > 0 {
		t.Fatalf("%d cells changed hands while both charging (stale boundary)", steals)
	}
	// 面积守恒
	if int(f.Area(1))+int(f.Area(2))+int(f.Area(0)) != inPotCells(f) {
		t.Fatal("area not conserved")
	}
	t.Logf("p1=%d p2=%d soup=%d", f.Area(1), f.Area(2), f.Area(0))
}

func TestT4StealSlower(t *testing.T) {
	// 双人相对中场，p2 充能至相遇后停止 → p1 抢 p2 地盘。
	// 验证 stealPenalty（D0001M05）生效：抢地路径 16 Chamfer/格，显著慢于开荒 8。
	f := newTestField()
	rate := fixed.FromFloat(0.25) // 加速（0.25 Chamfer/tick，32 tick 推进 1 格）
	f.Seed(1, Cell(48*gridW+30), seed10)
	f.Seed(2, Cell(48*gridW+66), seed10)
	var charging [4]bool
	charging[0], charging[1] = true, true
	tick := uint32(0)
	for ; tick < 200; tick++ { // 双方充能：圆盘相遇，边界僵持
		f.Step(charging, rate, tick)
	}
	charging[1] = false
	for ; tick < 300; tick++ { // p2 停止：p1 吃掉残余原汤、贴住 p2 边界
		f.Step(charging, rate, tick)
	}
	p1Before, p2Before := f.Area(1), f.Area(2)
	for ; tick < 700; tick++ { // 测量段：p1 同时开荒与抢地
		f.Step(charging, rate, tick)
	}
	stolen := p2Before - f.Area(2)
	if stolen <= 0 {
		t.Fatalf("no steal happened: p2 area %d → %d", p2Before, f.Area(2))
	}
	openClaim := (f.Area(1) - p1Before) - stolen
	stealRate := float64(stolen) / 400
	openRate := float64(openClaim) / 400
	if stealRate >= openRate*0.75 {
		t.Fatalf("steal %.2f flips/tick should be slower than open-claim %.2f flips/tick",
			stealRate, openRate)
	}
	t.Logf("steal %.2f/tick vs open-claim %.2f/tick (p2: %d → %d)", stealRate, openRate, p2Before, f.Area(2))
}

func TestT5DissolveNoHoles(t *testing.T) {
	f := newTestField()
	f.Seed(1, Cell(48*gridW+48), 2000)
	assertConnected(t, f, 1)
	dissolve := fixed.FromFloat(0.09) // D0001M07：r=30 格时 0.09 Chamfer/tick
	for tick := uint32(1); tick <= 500; tick++ {
		f.Dissolve(1, dissolve, tick)
		if f.Area(1) <= 0 {
			break
		}
		assertConnected(t, f, 1) // 流失不出现空洞（T5）
	}
	if f.Area(1) >= 2000 {
		t.Fatal("no dissolve happened")
	}
	t.Logf("after dissolve: area = %d", f.Area(1))
}

func TestT6Deterministic(t *testing.T) {
	run := func() uint64 {
		f := newTestField()
		seedCorners(f, seed10)
		rate := fixed.FromFloat(0.0625)
		var charging [4]bool
		for tick := uint32(1); tick <= 3000; tick++ {
			charging[0] = tick%7 != 0
			charging[1] = tick%13 < 10
			charging[2] = tick%5 < 2
			charging[3] = tick%11 < 6
			f.Step(charging, rate, tick)
			if tick%500 == 0 {
				f.Dissolve(2, fixed.FromFloat(0.05), tick)
			}
		}
		return f.Hash()
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("non-deterministic replay: %016x != %016x", a, b)
	}
}

func TestT7ZeroAlloc(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	var charging [4]bool
	for i := range charging {
		charging[i] = true // 全员充能 = 最恶劣路径（僵持/抢占/堆操作最多）
	}
	rate := fixed.FromFloat(0.0625)
	tick := uint32(0)
	// 预热 500 tick：堆、环形日志、bitset 进入稳态（预分配容量全部到位）
	for ; tick < 500; tick++ {
		f.Step(charging, rate, tick)
	}
	var changes []Change
	changes = make([]Change, 0, 4096)
	allocs := testing.AllocsPerRun(200, func() {
		tick++
		f.Step(charging, rate, tick)
		if !f.DiffSince(tick-100, &changes) {
			t.Fatal("window too old") // 不应发生：环容量 4096 >> 100 tick 变更量
		}
	})
	if allocs != 0 {
		t.Fatalf("Step+DiffSince allocs = %v, want 0", allocs)
	}
	t.Logf("zero alloc verified; blocked/tick headroom: front=%d claimed=%d",
		f.frontier[0].len(), f.claimed[0].len())
}

func TestT8Visualize(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	// 生成产物写临时目录（只读 CI 安全）；testdata/ 保留静态产物供肉眼查验。
	dir := t.TempDir()
	if err := f.PNG(filepath.Join(dir, "territory_t0000.png"), 4); err != nil {
		t.Fatal(err)
	}
	var charging [4]bool
	charging[0] = true
	charging[1] = true
	charging[3] = true
	charging[2] = true
	for tick := uint32(1); tick <= 1500; tick++ {
		f.Step(charging, expandRate, tick)
	}
	if err := f.PNG(filepath.Join(dir, "territory_t1500.png"), 4); err != nil {
		t.Fatal(err)
	}
	t.Logf("visualized: %s", filepath.Join(dir, "territory_t1500.png"))
	// 人工可读的 ASCII 快照（缩小到每 4 格 1 字符）
	var buf bytes.Buffer
	for y := 0; y < gridH; y += 4 {
		for x := 0; x < gridW; x += 4 {
			o := f.owner[y*gridW+x]
			if o == OwnerWall {
				buf.WriteByte('#')
			} else if o == OwnerSoup {
				buf.WriteByte('.')
			} else {
				buf.WriteByte('0' + o)
			}
		}
		buf.WriteByte('\n')
	}
	t.Logf("snapshot @1500:\n%s", buf.String())
}

// ---- 辅助断言 ----

func inPotCells(f *Field) int {
	n := 0
	for _, o := range f.owner {
		if o != OwnerWall {
			n++
		}
	}
	return n
}

// assertConnected BFS 检查 owner 的全部格与中心连通（T5：无空洞）。
func assertConnected(t *testing.T, f *Field, owner uint8) {
	t.Helper()
	var center Cell = 0
	found := 0
	for c := 0; c < f.n; c++ {
		if f.owner[c] == owner {
			center = Cell(c)
			found++
			break
		}
	}
	if found == 0 {
		return // 全流失
	}
	visited := make([]bool, f.n)
	queue := []Cell{center}
	visited[center] = true
	count := 1
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		var nb [8]nbr
		n := f.neighbors8(c, &nb)
		for i := 0; i < n; i++ {
			b := nb[i]
			if !visited[b.c] && f.owner[b.c] == owner {
				visited[b.c] = true
				count++
				queue = append(queue, b.c)
			}
		}
	}
	if count != int(f.Area(owner)) {
		t.Fatalf("owner %d has %d cells but only %d connected (holes!)", owner, f.Area(owner), count)
	}
}

// ---- 附加：DiffSince 语义与 RLE 关键帧 ----

func TestDiffSinceCumulative(t *testing.T) {
	f := newTestField()
	f.Seed(1, Cell(48*gridW+48), seed10)
	var charging [4]bool
	charging[0] = true
	var changes []Change
	for tick := uint32(1); tick <= 400; tick++ {
		f.Step(charging, expandRate, tick)
	}
	if !f.DiffSince(1, &changes) || len(changes) == 0 {
		t.Fatalf("DiffSince(1) = %v, %d changes", f.DiffSince(1, &changes), len(changes))
	}
	first := len(changes)
	// 继续 200 tick 后，DiffSince(1) 必须包含之前全部变更（同格取最新）
	for tick := uint32(401); tick <= 600; tick++ {
		f.Step(charging, expandRate, tick)
	}
	if !f.DiffSince(1, &changes) {
		t.Fatal("DiffSince(1) false")
	}
	second := len(changes)
	if second < first {
		t.Fatalf("cumulative diff shrank: %d → %d", first, second)
	}
	// DiffSince(100) 只含 tick > 100 的变更
	if !f.DiffSince(100, &changes) {
		t.Fatal("DiffSince(100) false")
	}
	for _, c := range changes {
		if c.Tick <= 100 {
			t.Fatalf("change tick %d <= since 100", c.Tick)
		}
	}
	// 幂等：同 since 两次结果一致
	if !f.DiffSince(100, &changes) {
		t.Fatal("DiffSince(100) false")
	}
	clone := make([]Change, len(changes))
	copy(clone, changes)
	if !f.DiffSince(100, &changes) {
		t.Fatal("DiffSince(100) false")
	}
	if len(changes) != len(clone) {
		t.Fatalf("idempotency broken: %d vs %d", len(changes), len(clone))
	}
}

func TestDiffSinceKeyframeFallback(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	var charging [4]bool
	for i := range charging {
		charging[i] = true
	}
	var changes []Change
	// 四人充能足够久：变更量超过 4096 环容量 → 窗口必然过期
	for tick := uint32(1); tick <= 4600; tick++ {
		f.Step(charging, expandRate, tick)
	}
	if f.log.n < len(f.log.buf) {
		t.Fatalf("ring not full: %d/%d — 用例没把窗口跑过期", f.log.n, len(f.log.buf))
	}
	// 超旧 since（早于环内最老记录）→ 必须返回 false（降级关键帧）
	if f.DiffSince(0, &changes) {
		t.Fatal("DiffSince(0) should be false when window expired")
	}
	// 环内窗口内的 since 仍可用
	oldest := f.log.oldestTick()
	if !f.DiffSince(oldest, &changes) {
		t.Fatal("DiffSince(oldest) should be true")
	}
}

// testWriter 适配 RLEWriter（小端）。
type testWriter struct{ bytes.Buffer }

func (w *testWriter) PutU16(v uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], v)
	w.Write(b[:])
}
func (w *testWriter) PutU8(v uint8) { w.WriteByte(v) }

func TestEncodeRLE(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	var w testWriter
	f.EncodeRLE(&w)
	data := w.Bytes()
	if len(data)%3 != 0 {
		t.Fatalf("RLE bytes %d not multiple of 3", len(data))
	}
	// 解码校验：总格数 = 9216，逐 run 与 owner 数组一致
	decoded := make([]uint8, 0, gridW*gridH)
	for i := 0; i < len(data); i += 3 {
		length := int(binary.LittleEndian.Uint16(data[i : i+2]))
		owner := data[i+2]
		for j := 0; j < length; j++ {
			decoded = append(decoded, owner)
		}
	}
	if len(decoded) != gridW*gridH {
		t.Fatalf("decoded %d cells, want %d", len(decoded), gridW*gridH)
	}
	for i, o := range decoded {
		if o != f.owner[i] {
			t.Fatalf("cell %d: RLE %d != field %d", i, o, f.owner[i])
		}
	}
	t.Logf("RLE: %d runs, %d bytes (full grid = 9216)", len(data)/3, len(data))
}

// TestPropertyAreaConserved 属性测试：随机（确定序列）输入下面积守恒、无空洞。
func TestPropertyAreaConserved(t *testing.T) {
	f := newTestField()
	seedCorners(f, seed10)
	rate := fixed.FromFloat(0.0625)
	var charging [4]bool
	r := uint32(12345)
	next := func() uint32 {
		r = r*1664525 + 1013904223
		return r
	}
	for tick := uint32(1); tick <= 2000; tick++ {
		for p := 0; p < 4; p++ {
			charging[p] = next()%3 != 0
		}
		f.Step(charging, rate, tick)
		if next()%17 == 0 {
			f.Dissolve(uint8(next()%4+1), fixed.FromFloat(0.03), tick)
		}
		if f.Area(1)+f.Area(2)+f.Area(3)+f.Area(4)+f.Area(0) != int32(inPotCells(f)) {
			t.Fatalf("tick %d: area not conserved", tick)
		}
	}
	// 最终面积非零且玩家面积总和守恒。
	// 注：连通性断言与 TestBoundaryTugOfWar 的「飞地」已知行为有语义张力——
	// 本用例 seed 固定（四角对称、中间隔原汤），随机充能序列下未观察到夹击
	// 挖穿，故仍断言连通；若改种子/序列后出现飞地，先核对是否为夹击几何结果。
	for p := uint8(1); p <= 4; p++ {
		assertConnected(t, f, p)
	}
}

// TestStepRates 逐玩家 rate：p1 基础速率、p2 双倍速率，同 tick 数下 p2 扩张更快。
func TestStepRates(t *testing.T) {
	f := newTestField()
	f.Seed(1, Cell(24*gridW+24), 400)
	f.Seed(2, Cell(24*gridW+72), 400)
	r1 := fixed.F(64)          // 0.0625
	r2 := fixed.F(160)         // 0.15625（×2.5）
	for tick := uint32(1); tick <= 2000; tick++ {
		f.StepRates([4]fixed.F{r1, r2, 0, 0}, tick)
	}
	if f.Area(1) >= f.Area(2) {
		t.Fatalf("p2 (×2.5 rate) should outpace p1: p1=%d p2=%d", f.Area(1), f.Area(2))
	}
	// 面积守恒
	if int(f.Area(1))+int(f.Area(2))+int(f.Area(0)) != inPotCells(f) {
		t.Fatal("area not conserved")
	}
}

// TestBoundaryTugOfWar 边界拉锯：同一格反复易主（抢占 + 抢回），
// 验证 claimed 堆 stale 条目不撑爆容量（2×n）、易主正确性、不 panic。
// 注：速率调低贴近真实（0.0625 Chamfer/tick），避免一方被吃光后
// frontier 塞满敌方边界导致僵持重试 O(2000/tick)（算法有上限，但测试要保持轻量）。
func TestBoundaryTugOfWar(t *testing.T) {
	f := newTestField()
	f.Seed(1, Cell(48*gridW+30), 600)
	f.Seed(2, Cell(48*gridW+66), 600)
	rate := expandRate
	var charging [4]bool
	charging[0], charging[1] = true, true
	tick := uint32(0)
	for ; tick < 200; tick++ { // 相遇（各推进 ~1.6 格）
		f.Step(charging, rate, tick)
	}
	for round := 0; round < 6; round++ { // 6 轮拉锯
		charging[1] = false // p1 抢
		for end := tick + 300; tick < end; tick++ {
			f.Step(charging, rate, tick)
		}
		charging[1] = true // p2 抢回
		charging[0] = false
		for end := tick + 300; tick < end; tick++ {
			f.Step(charging, rate, tick)
		}
		charging[0] = true
	}
	// 拉锯后面积守恒、双方都存活。
	// ⚠️ 不断言连通性：抢占是几何过程，双方从两侧夹击可能挖穿突出部形成
	// 「飞地」（孤岛）。规格 T0004M03F01 伪码无防夹击机制，飞地随后会被
	// 对方从边缘蚕食（其格在对方 frontier 中），面积会计仍正确。
	// 若 P0 实测视觉不可接受，再实现「空洞愈合」机制（见 AGENTS.md 已知行为）。
	if int(f.Area(1))+int(f.Area(2))+int(f.Area(0)) != inPotCells(f) {
		t.Fatalf("area not conserved: p1=%d p2=%d soup=%d", f.Area(1), f.Area(2), f.Area(0))
	}
	if f.Area(1) <= 0 || f.Area(2) <= 0 {
		t.Fatalf("tug-of-war ended with a player wiped out (p1=%d p2=%d)", f.Area(1), f.Area(2))
	}
	// stale 条目确实在增长但不爆容量
	t.Logf("after 6 rounds: p1=%d p2=%d, claimed1=%d (cap %d)", f.Area(1), f.Area(2), f.claimed[0].len(), cap(f.claimed[0].buf))
}
