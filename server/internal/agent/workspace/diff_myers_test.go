package workspace

import (
	"math/rand"
	"strings"
	"testing"
)

func applyOps(ops []diffOp) (oldText, newText []string) {
	for _, op := range ops {
		if op.kind != '+' {
			oldText = append(oldText, op.text)
		}
		if op.kind != '-' {
			newText = append(newText, op.text)
		}
	}
	return oldText, newText
}

func TestMyersReconstructsRandomInputs(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "b", "c", "d", ""}
	for i := 0; i < 500; i++ {
		oldLines := make([]string, rng.Intn(30))
		newLines := make([]string, rng.Intn(30))
		for j := range oldLines {
			oldLines[j] = alphabet[rng.Intn(len(alphabet))]
		}
		for j := range newLines {
			newLines[j] = alphabet[rng.Intn(len(alphabet))]
		}
		gotOld, gotNew := applyOps(myers(oldLines, newLines))
		if strings.Join(gotOld, "\n") != strings.Join(oldLines, "\n") || len(gotOld) != len(oldLines) ||
			strings.Join(gotNew, "\n") != strings.Join(newLines, "\n") || len(gotNew) != len(newLines) {
			t.Fatalf("第 %d 组随机输入脚本无法还原: old=%q new=%q", i, oldLines, newLines)
		}
	}
}

func TestMyersHugeDifferenceDegradesToReplace(t *testing.T) {
	oldLines := make([]string, 6000)
	newLines := make([]string, 6000)
	for i := range oldLines {
		oldLines[i] = "old-" + strings.Repeat("x", i%7) + string(rune('a'+i%26))
		newLines[i] = "new-" + strings.Repeat("y", i%5) + string(rune('a'+i%26))
	}
	ops := myers(oldLines, newLines)
	gotOld, gotNew := applyOps(ops)
	if len(gotOld) != len(oldLines) || len(gotNew) != len(newLines) {
		t.Fatalf("退化后脚本长度不对: %d/%d", len(gotOld), len(gotNew))
	}
}
