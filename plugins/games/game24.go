// game24.go 24 点的核心逻辑：出题（保证有解）、表达式解析与精确验算、求解器。
package games

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
)

const (
	exprMaxLen    = 64  // 表达式最大长度
	exprMaxNumber = 999 // 表达式中允许的最大数字
)

// ---------- 求解器 ----------

// exprItem 求解过程中的中间项：当前值 + 表达式串。
type exprItem struct {
	f frac
	s string
}

// solve24 求一组数字算出 24 的一个解，返回表达式串（找不到返回 false）。
// 每步任取两项合并（+ - * / 六个方向），直到只剩一项且等于 24。
func solve24(nums []int) (string, bool) {
	items := make([]exprItem, len(nums))
	for i, n := range nums {
		items[i] = exprItem{f: frac{num: int64(n), den: 1}, s: strconv.Itoa(n)}
	}
	if s, ok := solveItems(items); ok {
		return trimOuterParens(s), true
	}
	return "", false
}

// solveItems 递归求解。
func solveItems(items []exprItem) (string, bool) {
	if len(items) == 1 {
		if items[0].f.isInt(24) {
			return items[0].s, true
		}
		return "", false
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			rest := make([]exprItem, 0, len(items)-2)
			for k := range items {
				if k != i && k != j {
					rest = append(rest, items[k])
				}
			}

			cands := make([]exprItem, 0, 6)
			for _, op := range []string{"+", "-", "*", "/"} {
				if c, err := combine(a, b, op); err == nil {
					cands = append(cands, c)
				}
			}
			for _, op := range []string{"-", "/"} {
				if c, err := combine(b, a, op); err == nil {
					cands = append(cands, c)
				}
			}

			for _, c := range cands {
				next := append([]exprItem{c}, rest...)
				if s, ok := solveItems(next); ok {
					return s, true
				}
			}
		}
	}
	return "", false
}

// combine 合并两项：a op b。
func combine(a, b exprItem, op string) (exprItem, error) {
	var f frac
	var err error
	switch op {
	case "+":
		f, err = a.f.add(b.f)
	case "-":
		f, err = a.f.sub(b.f)
	case "*":
		f, err = a.f.mul(b.f)
	case "/":
		f, err = a.f.div(b.f)
	}
	if err != nil {
		return exprItem{}, err
	}
	return exprItem{f: f, s: "(" + a.s + op + b.s + ")"}, nil
}

// trimOuterParens 去掉整体包裹的一层括号。
func trimOuterParens(s string) string {
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		depth := 0
		wrapped := true
		for i, r := range s {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 && i != len(s)-1 {
					wrapped = false
				}
			}
			if !wrapped {
				break
			}
		}
		if !wrapped {
			break
		}
		s = s[1 : len(s)-1]
	}
	return s
}

// ---------- 表达式解析与验算 ----------

// eval24 解析四则表达式，返回计算结果与用到的数字列表。
// 文法：expr := term (('+'|'-') term)*；term := factor (('*'|'/') factor)*；
// factor := 数字 | '(' expr ')' | '-' factor。
// 兼容全角运算符与括号（＋－×÷（））。
func eval24(expr string) (frac, []int, error) {
	src := normalizeExpr(expr)
	if src == "" {
		return frac{}, nil, fmt.Errorf("表达式为空")
	}
	if len(src) > exprMaxLen {
		return frac{}, nil, fmt.Errorf("表达式太长")
	}

	p := &exprParser{src: src}
	p.skipSpace()
	v, err := p.parseExpr()
	if err != nil {
		return frac{}, nil, err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return frac{}, nil, fmt.Errorf("表达式有多余内容：%s", p.src[p.pos:])
	}
	return v, p.used, nil
}

type exprParser struct {
	src  string
	pos  int
	used []int
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *exprParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

func (p *exprParser) parseExpr() (frac, error) {
	left, err := p.parseTerm()
	if err != nil {
		return frac{}, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '+':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return frac{}, err
			}
			if left, err = left.add(right); err != nil {
				return frac{}, err
			}
		case '-':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return frac{}, err
			}
			if left, err = left.sub(right); err != nil {
				return frac{}, err
			}
		default:
			return left, nil
		}
	}
}

func (p *exprParser) parseTerm() (frac, error) {
	left, err := p.parseFactor()
	if err != nil {
		return frac{}, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '*':
			p.pos++
			right, err := p.parseFactor()
			if err != nil {
				return frac{}, err
			}
			if left, err = left.mul(right); err != nil {
				return frac{}, err
			}
		case '/':
			p.pos++
			right, err := p.parseFactor()
			if err != nil {
				return frac{}, err
			}
			if left, err = left.div(right); err != nil {
				return frac{}, fmt.Errorf("除数为零")
			}
		default:
			return left, nil
		}
	}
}

func (p *exprParser) parseFactor() (frac, error) {
	p.skipSpace()
	switch c := p.peek(); {
	case c == '(':
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return frac{}, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return frac{}, fmt.Errorf("括号不匹配")
		}
		p.pos++
		return v, nil
	case c == '-':
		p.pos++
		v, err := p.parseFactor()
		if err != nil {
			return frac{}, err
		}
		return newFrac(-v.num, v.den)
	case c >= '0' && c <= '9':
		start := p.pos
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
		n, err := strconv.Atoi(p.src[start:p.pos])
		if err != nil || n > exprMaxNumber {
			return frac{}, fmt.Errorf("数字太大")
		}
		p.used = append(p.used, n)
		return frac{num: int64(n), den: 1}, nil
	default:
		return frac{}, fmt.Errorf("无法识别的字符 %q", string(rune(c)))
	}
}

// normalizeExpr 把全角运算符/括号替换为半角，便于解析。
func normalizeExpr(s string) string {
	r := strings.NewReplacer(
		"＋", "+", "－", "-", "×", "*", "x", "*", "X", "*", "÷", "/",
		"（", "(", "）", ")", "　", " ",
	)
	return r.Replace(strings.TrimSpace(s))
}

// ---------- 出题 ----------

// gen24 生成一组保证有解的 4 个数字（1..maxN）。
func gen24(maxN int) []int {
	if maxN < 1 {
		maxN = 13
	}
	for try := 0; try < 400; try++ {
		nums := []int{
			1 + rand.Intn(maxN),
			1 + rand.Intn(maxN),
			1 + rand.Intn(maxN),
			1 + rand.Intn(maxN),
		}
		if _, ok := solve24(nums); ok {
			return nums
		}
	}
	return []int{3, 3, 8, 8} // 兜底经典题
}

// sameMultiset 判断两组数字是否为同一多重集（每个数恰好用一次）。
func sameMultiset(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]int(nil), a...)
	sb := append([]int(nil), b...)
	sortInts(sa)
	sortInts(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
