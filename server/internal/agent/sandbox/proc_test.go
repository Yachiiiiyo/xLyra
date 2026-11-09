package sandbox

import "testing"

// TestParseProcStat 覆盖进程名含空格与括号的情形:必须按第一个 '(' 与
// 最后一个 ')' 切分。
func TestParseProcStat(t *testing.T) {
	cases := []struct {
		name    string
		content string
		pid     int
		comm    string
		state   string
		ppid    int
		fail    bool
	}{
		{"普通", "3748 (cat) R 1234 1 1 0 - -", 3748, "cat", "R", 1234, false},
		{"带空格", "99 (my prog) S 1 0 0", 99, "my prog", "S", 1, false},
		{"带括号", "123 ((session)) D 4 0", 123, "(session)", "D", 4, false},
		{"两者都有", "7 (a (b) c) Z 8 0", 7, "a (b) c", "Z", 8, false},
		{"缺 comm", "123 R 1", 0, "", "", 0, true},
		{"字段不足", "5 (x) R", 0, "", "", 0, true},
		{"ppid 非数字", "5 (x) R one", 0, "", "", 0, true},
	}
	for _, tc := range cases {
		pid, comm, state, ppid, err := parseProcStat(tc.content)
		if tc.fail {
			if err == nil {
				t.Errorf("%s:应报错", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s:%v", tc.name, err)
			continue
		}
		if pid != tc.pid || comm != tc.comm || state != tc.state || ppid != tc.ppid {
			t.Errorf("%s:得到 pid=%d comm=%q state=%q ppid=%d", tc.name, pid, comm, state, ppid)
		}
	}
}

func TestParseStatusUids(t *testing.T) {
	content := "Name:\tgo\nUmask:\t0022\nState:\tS (sleeping)\nUid:\t30100\t30101\t0\t30100\nGid:\t30100\t30100\t30100\t30100\n"
	uids, ok := parseStatusUids(content)
	if !ok {
		t.Fatal("应解析成功")
	}
	if uids != [4]int{30100, 30101, 0, 30100} {
		t.Fatalf("uid 解析不符:%v", uids)
	}
	if _, ok := parseStatusUids("Name:\tgo\n"); ok {
		t.Fatal("缺 Uid 行应失败")
	}
	if _, ok := parseStatusUids("Uid:\t1 2\n"); ok {
		t.Fatal("Uid 字段不足应失败")
	}
}
