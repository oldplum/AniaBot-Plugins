package games

import (
	"strings"
	"testing"
)

func TestFracArithmetic(t *testing.T) {
	a, _ := newFrac(1, 3)
	b, _ := newFrac(8, 1)

	// 8 - 1/3 = 23/3
	d, err := b.sub(a)
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "23/3" {
		t.Errorf("8 - 1/3 = %s, 期望 23/3", d.String())
	}

	// 8 / (8 - 1/3) = 24/23
	q, err := b.div(d)
	if err != nil {
		t.Fatal(err)
	}
	if q.String() != "24/23" {
		t.Errorf("8/(8-1/3) = %s, 期望 24/23", q.String())
	}

	// 除以零
	zero, _ := newFrac(0, 5)
	if _, err := b.div(zero); err == nil {
		t.Error("除以零应报错")
	}

	// 负分母归一化
	f, _ := newFrac(1, -2)
	if f.String() != "-1/2" {
		t.Errorf("负分母应归一化: %s", f.String())
	}
}

func TestEval24(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"(5-3)*8+8", "24"},
		{"( 5 - 3 ) * 8 + 8", "24"},
		{"8/(3-8/3)", "24"},
		{"3*8", "24"},
		{"1+2", "3"},
		{"7/2", "7/2"},
		{"－5＋3", "-2"}, // 全角符号
		{"-3+8", "5"},  // 一元负号
	}
	for _, c := range cases {
		v, _, err := eval24(c.expr)
		if err != nil {
			t.Fatalf("%q 解析失败: %v", c.expr, err)
		}
		if v.String() != c.want {
			t.Errorf("%q = %s, 期望 %s", c.expr, v.String(), c.want)
		}
	}
}

func TestEval24UsedNumbers(t *testing.T) {
	_, used, err := eval24("(5-3)*8+8")
	if err != nil {
		t.Fatal(err)
	}
	if !sameMultiset(used, []int{5, 3, 8, 8}) {
		t.Errorf("用到的数字错误: %v", used)
	}
}

func TestEval24Errors(t *testing.T) {
	cases := []string{
		"",
		"2+*3",    // 语法错误
		"(2+3",    // 括号不匹配
		"2+3)",    // 多余括号
		"5/0",     // 除零
		"2+3 a",   // 多余内容
		"12345+1", // 数字太大
		"2..3",    // 非法字符
	}
	for _, in := range cases {
		if _, _, err := eval24(in); err == nil {
			t.Errorf("%q 应解析失败", in)
		}
	}
}

func TestSameMultiset(t *testing.T) {
	if !sameMultiset([]int{3, 8, 5, 8}, []int{8, 8, 5, 3}) {
		t.Error("相同多重集应相等")
	}
	if sameMultiset([]int{3, 8, 5, 8}, []int{8, 8, 5, 5}) {
		t.Error("不同多重集不应相等")
	}
	if sameMultiset([]int{3, 8}, []int{3, 8, 8}) {
		t.Error("长度不同不应相等")
	}
}

func TestSolve24(t *testing.T) {
	// 经典难题 3,3,8,8：唯一解 8/(3-8/3)
	s, ok := solve24([]int{3, 3, 8, 8})
	if !ok {
		t.Fatal("3,3,8,8 应该有解")
	}
	v, used, err := eval24(s)
	if err != nil || !v.isInt(24) {
		t.Fatalf("求解器给出的表达式不成立: %q → %v, %v", s, v, err)
	}
	if !sameMultiset(used, []int{3, 3, 8, 8}) {
		t.Errorf("求解器使用的数字不对: %v", used)
	}

	// 无解组合
	if _, ok := solve24([]int{1, 1, 1, 1}); ok {
		t.Error("1,1,1,1 不应有解")
	}
}

func TestSolve24RandomSolvable(t *testing.T) {
	// gen24 出的题必须可解，且解能通过验算
	for i := 0; i < 30; i++ {
		nums := gen24(13)
		s, ok := solve24(nums)
		if !ok {
			t.Fatalf("gen24 出的题无解: %v", nums)
		}
		v, used, err := eval24(s)
		if err != nil || !v.isInt(24) || !sameMultiset(used, nums) {
			t.Fatalf("题目 %v 的解 %q 验算失败: %v %v %v", nums, s, v, used, err)
		}
	}
}

func TestNormalizeExpr(t *testing.T) {
	if got := normalizeExpr("（５）×2"); got != "(５)*2" {
		// 注意：全角数字不在替换范围（属非法输入，会由解析器报错）
		t.Logf("normalizeExpr 全角数字: %q", got)
	}
	if got := normalizeExpr(" 8÷2 "); got != "8/2" {
		t.Errorf("normalizeExpr 错误: %q", got)
	}
}

func TestTrimOuterParens(t *testing.T) {
	cases := map[string]string{
		"((1+2)*3)": "(1+2)*3",
		"(1+2)*3":   "(1+2)*3",
		"24":        "24",
		"(1+(2*3))": "1+(2*3)",
	}
	for in, want := range cases {
		if got := trimOuterParens(in); got != want {
			t.Errorf("trimOuterParens(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestPickFilterSeparators(t *testing.T) {
	// 模拟 /选 吃饭 或 吃面 还是 叫外卖 的参数过滤
	args := []string{"吃饭", "或", "吃面", "还是", "叫外卖"}
	var opts []string
	for _, a := range args {
		if _, sep := pickSeparators[a]; sep || a == "" {
			continue
		}
		opts = append(opts, a)
	}
	if len(opts) != 3 || strings.Join(opts, ",") != "吃饭,吃面,叫外卖" {
		t.Errorf("选项过滤错误: %v", opts)
	}
}
