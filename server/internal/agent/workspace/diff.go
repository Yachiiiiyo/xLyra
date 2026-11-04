package workspace

import (
	"fmt"
	"strings"
)

type diffOp struct {
	kind byte
	text string
}

func lines(text string) []string {
	if text == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// myersMaxD 限制 Myers 的最大编辑距离:trace 内存是 O(D²),超限退化为整段替换。
const myersMaxD = 2000

// myers 计算行级编辑脚本:先剥掉公共前后缀,再对中间部分做 Myers;
// 差异过大(超过 myersMaxD)时把中间部分当作整段删除 + 整段新增。
func myers(oldLines, newLines []string) []diffOp {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	midOld := oldLines[prefix : len(oldLines)-suffix]
	midNew := newLines[prefix : len(newLines)-suffix]
	ops := make([]diffOp, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines[:prefix] {
		ops = append(ops, diffOp{' ', line})
	}
	mid, ok := myersCore(midOld, midNew)
	if !ok {
		mid = mid[:0]
		for _, line := range midOld {
			mid = append(mid, diffOp{'-', line})
		}
		for _, line := range midNew {
			mid = append(mid, diffOp{'+', line})
		}
	}
	ops = append(ops, mid...)
	for _, line := range oldLines[len(oldLines)-suffix:] {
		ops = append(ops, diffOp{' ', line})
	}
	return ops
}

// myersCore 是标准 Myers 最短编辑脚本;trace[d] 只保存 k∈[-d,d] 的 v 值。
// 编辑距离超过 myersMaxD 时返回 ok=false。
func myersCore(oldLines, newLines []string) ([]diffOp, bool) {
	n, m := len(oldLines), len(newLines)
	if n+m == 0 {
		return nil, true
	}
	limit := n + m
	if limit > myersMaxD {
		limit = myersMaxD
	}
	offset := limit + 1
	v := make([]int, 2*limit+3)
	trace := make([][]int, 0, limit+1)
	end := -1
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && oldLines[x] == newLines[y] {
				x, y = x+1, y+1
			}
			v[offset+k] = x
			if x >= n && y >= m {
				end = d
				break
			}
		}
		// trace[d] 记录第 d 轮结束后的 v(只含 [-d,d]),回溯第 d 步时读取 trace[d-1] 的前驱
		trace = append(trace, append([]int(nil), v[offset-d-1:offset+d+2]...))
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil, false
	}
	at := func(d, k int) int { return trace[d][k+d+1] }
	ops := make([]diffOp, 0, n+m)
	x, y := n, m
	for d := end; d > 0; d-- {
		k := x - y
		var prevK int
		if k == -d || (k != d && at(d-1, k-1) < at(d-1, k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(d-1, prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			ops = append(ops, diffOp{' ', oldLines[x-1]})
			x, y = x-1, y-1
		}
		if x == prevX {
			ops = append(ops, diffOp{'+', newLines[y-1]})
			y--
		} else {
			ops = append(ops, diffOp{'-', oldLines[x-1]})
			x--
		}
	}
	for x > 0 && y > 0 {
		ops = append(ops, diffOp{' ', oldLines[x-1]})
		x, y = x-1, y-1
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops, true
}

func UnifiedDiff(path string, oldText, newText string) (patch string, additions, deletions int, truncated bool) {
	ops := myers(lines(oldText), lines(newText))
	for _, op := range ops {
		if op.kind == '+' {
			additions++
		} else if op.kind == '-' {
			deletions++
		}
	}
	if additions == 0 && deletions == 0 {
		return "", 0, 0, false
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	changed, size := 0, out.Len()
	for start := 0; start < len(ops); {
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start >= len(ops) {
			break
		}
		from := start - 3
		if from < 0 {
			from = 0
		}
		to, last := start+1, start
		for to < len(ops) {
			if ops[to].kind != ' ' {
				last = to
			}
			if to-last > 6 {
				break
			}
			to++
		}
		if to > len(ops) {
			to = len(ops)
		}
		if last+3 < to {
			to = last + 4
		}
		oldStart, newStart := 1, 1
		for _, op := range ops[:from] {
			if op.kind != '+' {
				oldStart++
			}
			if op.kind != '-' {
				newStart++
			}
		}
		oldCount, newCount := 0, 0
		for _, op := range ops[from:to] {
			if op.kind != '+' {
				oldCount++
			}
			if op.kind != '-' {
				newCount++
			}
		}
		if oldCount == 0 {
			oldStart = 0
		}
		if newCount == 0 {
			newStart = 0
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		if size+len(header) > 200<<10 || changed >= 2000 {
			truncated = true
			break
		}
		out.WriteString(header)
		size += len(header)
		for _, op := range ops[from:to] {
			line := string(op.kind) + op.text + "\n"
			if size+len(line) > 200<<10 || (op.kind != ' ' && changed >= 2000) {
				truncated = true
				break
			}
			out.WriteString(line)
			size += len(line)
			if op.kind != ' ' {
				changed++
			}
		}
		start = to
	}
	return out.String(), additions, deletions, truncated
}

func Stats(oldText, newText string) (additions, deletions int) {
	for _, op := range myers(lines(oldText), lines(newText)) {
		if op.kind == '+' {
			additions++
		} else if op.kind == '-' {
			deletions++
		}
	}
	return additions, deletions
}
