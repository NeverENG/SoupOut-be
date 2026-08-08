package fixed

import "testing"

func TestBasics(t *testing.T) {
	if One.ToInt() != 1 {
		t.Fatalf("One.ToInt() = %d, want 1", One.ToInt())
	}
	if I(5) != 5*One {
		t.Fatal("I(5) != 5*One")
	}
	if got := I(3).Add(I(4)); got != I(7) {
		t.Fatalf("3+4 = %v, want 7", got.ToFloat())
	}
	if got := I(3).Sub(I(4)); got != I(-1) {
		t.Fatalf("3-4 = %v, want -1", got.ToFloat())
	}
	if got := I(3).Neg(); got != I(-3) {
		t.Fatalf("-3 = %v", got.ToFloat())
	}
	if got := I(-7).Abs(); got != I(7) {
		t.Fatalf("|-7| = %v", got.ToFloat())
	}
}

func TestMulDiv(t *testing.T) {
	// Mul 用例因子均与 Q22.10 友好（定点量化误差 < 0.1%），避免 0.01 这类病态小数。
	mulCases := []struct {
		a, b float64
	}{
		{0.0625, 3}, {6.0, 0.45}, {15, 1.35}, {0.8, 0.9}, {-2.5, 4}, {3.5, 2.5},
		{6.0, 0.0625}, {100, 0.0625}, {0.25, 8},
	}
	for _, c := range mulCases {
		a, b := FromFloat(c.a), FromFloat(c.b)
		if got := a.Mul(b).ToFloat(); !closeEnough(got, c.a*c.b) {
			t.Errorf("%v × %v = %v, want ≈%v", c.a, c.b, got, c.a*c.b)
		}
	}
	// Div 对量化误差更敏感，单独用精确可表示因子。
	divCases := []struct {
		a, b float64
	}{
		{6.0, 0.5}, {15, 1.5}, {1, 4}, {3.5, 0.25}, {-5, 2}, {0.0625, 0.25},
	}
	for _, c := range divCases {
		a, b := FromFloat(c.a), FromFloat(c.b)
		if b != 0 && !closeEnough(a.Div(b).ToFloat(), c.a/c.b) {
			t.Errorf("%v ÷ %v = %v, want ≈%v", c.a, c.b, a.Div(b).ToFloat(), c.a/c.b)
		}
	}
	// 乘积落在 Q22.10 值域内时不溢出 int32（int64 中间量）
	big := I(100).Mul(I(100))
	if big.ToInt() != 10_000 {
		t.Fatalf("100×100 = %d, want 10000", big.ToInt())
	}
	// 除零不 panic
	if got := I(1).Div(0); got != 0 {
		t.Fatalf("Div(0) = %v, want 0", got)
	}
}

func TestRoundTrip(t *testing.T) {
	vals := []float64{0, 1, -1, 0.5, -0.5, 1.5, 3.75, 0.0625, 100.999, -2048}
	for _, v := range vals {
		got := FromFloat(v).ToFloat()
		if !closeEnough(got, v) {
			t.Errorf("roundtrip(%v) = %v", v, got)
		}
	}
}

func TestFloorCeil(t *testing.T) {
	cases := []struct {
		v           float64
		floor, ceil int32
	}{
		{2.0, 2, 2}, {2.5, 2, 3}, {-2.0, -2, -2}, {-2.5, -3, -2},
		{0.0, 0, 0}, {0.5, 0, 1}, {-0.5, -1, 0},
	}
	for _, c := range cases {
		f := FromFloat(c.v)
		if got := f.Floor(); got != c.floor {
			t.Errorf("Floor(%v) = %d, want %d", c.v, got, c.floor)
		}
		if got := f.Ceil(); got != c.ceil {
			t.Errorf("Ceil(%v) = %d, want %d", c.v, got, c.ceil)
		}
	}
}

func TestSqrt(t *testing.T) {
	for _, v := range []float64{0, 1, 4, 2.25, 0.25, 12345, 1e6} {
		got := FromFloat(v).Sqrt().ToFloat()
		want := sqrtApprox(v)
		if !closeEnough(got, want) {
			t.Errorf("Sqrt(%v) = %v, want ≈%v", v, got, want)
		}
	}
}

func TestVec2(t *testing.T) {
	v := Vec2{I(3), I(4)}
	if got := v.Len().ToFloat(); !closeEnough(got, 5) {
		t.Fatalf("Len((3,4)) = %v, want 5", got)
	}
	if got := v.Len2(); got != I(25) {
		t.Fatalf("Len2 = %v, want 25", got.ToFloat())
	}
	n := v.Normalized()
	if !closeEnough(n.Len().ToFloat(), 1) {
		t.Fatalf("Normalized().Len() = %v, want 1", n.Len().ToFloat())
	}
	if got := v.Dot(Vec2{I(1), I(0)}); got != I(3) {
		t.Fatalf("Dot = %v, want 3", got.ToFloat())
	}
	if got := v.Add(Vec2{I(1), I(-1)}); got != (Vec2{I(4), I(3)}) {
		t.Fatalf("Add = %+v", got)
	}
	if got := v.Sub(Vec2{I(1), I(1)}); got != (Vec2{I(2), I(3)}) {
		t.Fatalf("Sub = %+v", got)
	}
	zero := Vec2{}
	if z := zero.Normalized(); z != zero {
		t.Fatalf("zero.Normalized() = %+v, want zero", z)
	}
	if got := (Vec2{I(1), I(2)}).Mul(I(3)); got != (Vec2{I(3), I(6)}) {
		t.Fatalf("Mul = %+v", got)
	}
}

func closeEnough(a, b float64) bool {
	// Q22.10 每级操作量化误差 ≤ 1/1024 ≈ 0.001；两级（FromFloat→运算）累积 < 0.02。
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 0.02
}

// sqrtApprox 是测试用的浮点参考实现（测试代码允许浮点）。
func sqrtApprox(v float64) float64 {
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 60; i++ {
		x = (x + v/x) / 2
	}
	return x
}
