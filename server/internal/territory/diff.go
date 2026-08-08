package territory

// Change 是一次格归属变更（变更环形日志条目）。
type Change struct {
	Tick  uint32
	Cell  Cell
	Owner uint8
}

// changeRing 是容量固定的环形变更日志。容量 4096 ≈ 40s 历史（T0004M09 待压测调整）。
// DiffSince 从环尾（最新）向前走，开销只与变更数成正比（BE0000M05⑤）。
type changeRing struct {
	buf  []Change
	head int // 最新记录下标；-1 = 空
	n    int
}

func (r *changeRing) init(cap int) {
	r.buf = make([]Change, cap)
	r.head = -1
	r.n = 0
}

func (r *changeRing) record(c Change) {
	r.head++
	if r.head == len(r.buf) {
		r.head = 0
	}
	r.buf[r.head] = c
	if r.n < len(r.buf) {
		r.n++
	}
}

// oldestTick 返回环内最老记录的 tick（空环返回 0）。
func (r *changeRing) oldestTick() uint32 {
	if r.n == 0 {
		return 0
	}
	idx := r.head - (r.n - 1)
	if idx < 0 {
		idx += len(r.buf)
	}
	return r.buf[idx].Tick
}

// forEachNewerThan 从最新往最旧遍历 Tick > since 的记录并交给 visit。
func (r *changeRing) forEachNewerThan(since uint32, visit func(Change) bool) {
	for i := 0; i < r.n; i++ {
		idx := r.head - i
		if idx < 0 {
			idx += len(r.buf)
		}
		c := r.buf[idx]
		if c.Tick <= since {
			return
		}
		if !visit(c) {
			return
		}
	}
}

// DiffSince 收集 Tick > sinceTick 的全部变更（同一格取最新，复用 epoch
// bitset 去重，零分配）。out 是复用切片（cap 不足才会扩容——调用方应预分配）。
// 返回 false = sinceTick 早于环形窗口，必须改推关键帧（T0001M03F06）。
// 完整性判定：环内最老记录 tick 为 oldest，since+1 >= oldest 即覆盖完整
// （环里含 [oldest, now] 的全部变更；since 只需要 > since 的部分）。
func (f *Field) DiffSince(sinceTick uint32, out *[]Change) bool {
	*out = (*out)[:0]
	if f.log.n == 0 {
		return true // 从未有变更：任意 since 都完整
	}
	if uint64(sinceTick)+1 < uint64(f.log.oldestTick()) {
		return false
	}
	f.diffEpoch++
	ep := f.diffEpoch
	f.log.forEachNewerThan(sinceTick, func(c Change) bool {
		if f.diffStamp[c.Cell] != ep {
			f.diffStamp[c.Cell] = ep
			*out = append(*out, c)
		}
		return true
	})
	return true
}
