package territory

// 堆元素打包：uint32 = dist<<16 | cell。
// 一次比较同时按 dist 再按 cell 排序，平局顺序确定（T0004M03F01 · T0004M05）。
//
// 不用 container/heap：其接口方法（Push(any)/Pop() any）有装箱开销，
// 且热路径要求零分配（T7）。两个堆均预分配容量，push 满容量时 append 会扩容
// 触发分配 —— 容量必须由 New/Seed 给足（frontier 2048 / claimed 18432），
// 由 TestStepZeroAlloc 兜底断言。

// minHeap：dist 小优先（平局 cell 小优先），用于扩张前沿。
type minHeap struct{ buf []uint32 }

func (h *minHeap) init(cap int) { h.buf = make([]uint32, 0, cap) }
func (h *minHeap) len() int      { return len(h.buf) }
func (h *minHeap) top() uint32   { return h.buf[0] }

func (h *minHeap) push(v uint32) {
	if len(h.buf) == cap(h.buf) {
		// 不变量：预分配容量必须够（frontier 2048 / claimed 18432 = 2×网格格数）。
		// 触发即容量分析失效，panic 早暴露而不是静默扩容（扩容 = 分配 = 违反 T7）。
		panic("territory: minHeap capacity exceeded")
	}
	h.buf = append(h.buf, v)
	i := len(h.buf) - 1
	for i > 0 {
		par := (i - 1) / 2
		if h.buf[par] <= h.buf[i] {
			break
		}
		h.buf[par], h.buf[i] = h.buf[i], h.buf[par]
		i = par
	}
}

func (h *minHeap) pop() uint32 {
	v := h.buf[0]
	last := len(h.buf) - 1
	h.buf[0] = h.buf[last]
	h.buf = h.buf[:last]
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		if l >= len(h.buf) {
			break
		}
		m := l
		if r < len(h.buf) && h.buf[r] < h.buf[l] {
			m = r
		}
		if h.buf[m] >= h.buf[i] {
			break
		}
		h.buf[m], h.buf[i] = h.buf[i], h.buf[m]
		i = m
	}
	return v
}

// maxHeap：dist 大优先（平局 cell 大优先），用于流失时从最外层退。
type maxHeap struct{ buf []uint32 }

func (h *maxHeap) init(cap int) { h.buf = make([]uint32, 0, cap) }
func (h *maxHeap) len() int      { return len(h.buf) }
func (h *maxHeap) top() uint32   { return h.buf[0] }

func (h *maxHeap) push(v uint32) {
	if len(h.buf) == cap(h.buf) {
		// 不变量：预分配容量必须够（claimed = 网格格数）。触发即算法/容量 bug，尽早暴露。
		panic("territory: maxHeap capacity exceeded")
	}
	h.buf = append(h.buf, v)
	i := len(h.buf) - 1
	for i > 0 {
		par := (i - 1) / 2
		if h.buf[par] >= h.buf[i] {
			break
		}
		h.buf[par], h.buf[i] = h.buf[i], h.buf[par]
		i = par
	}
}

func (h *maxHeap) pop() uint32 {
	v := h.buf[0]
	last := len(h.buf) - 1
	h.buf[0] = h.buf[last]
	h.buf = h.buf[:last]
	i := 0
	for {
		l, r := 2*i+1, 2*i+2
		if l >= len(h.buf) {
			break
		}
		m := l
		if r < len(h.buf) && h.buf[r] > h.buf[l] {
			m = r
		}
		if h.buf[m] <= h.buf[i] {
			break
		}
		h.buf[m], h.buf[i] = h.buf[i], h.buf[m]
		i = m
	}
	return v
}
