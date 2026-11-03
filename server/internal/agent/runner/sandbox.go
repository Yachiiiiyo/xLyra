package runner

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// piReadOnlyPaths 计算 Pi 运行所需的额外只读路径;不存在的路径直接忽略。
func piReadOnlyPaths(command, piEntry, extension string, skillDirs []string, pathEnv string) []string {
	var paths []string
	add := func(path string) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		for _, existing := range paths {
			if existing == path {
				return
			}
		}
		paths = append(paths, path)
	}
	if command != "" {
		if node, err := exec.LookPath(command); err == nil {
			if real, err := filepath.EvalSymlinks(node); err == nil {
				node = real
			}
			dir := filepath.Dir(node)
			if filepath.Base(dir) == "bin" {
				dir = filepath.Dir(dir)
			}
			add(dir)
		}
	}
	if piEntry != "" && !strings.HasPrefix(piEntry, "-") {
		entry := piEntry
		if real, err := filepath.EvalSymlinks(entry); err == nil {
			entry = real
		}
		root := filepath.Dir(entry)
		for dir := root; ; dir = filepath.Dir(dir) {
			if filepath.Base(dir) == "node_modules" {
				root = filepath.Dir(dir)
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
		add(root)
	}
	if extension != "" {
		add(filepath.Dir(extension))
	}
	for _, dir := range skillDirs {
		add(dir)
		add(filepath.Dir(dir))
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		add(dir)
	}
	return paths
}

func gatewayConnectPort(raw string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("runner: 解析网关地址: %w", err)
	}
	if u.Hostname() == "" {
		return 0, fmt.Errorf("runner: 网关地址缺少主机: %q", raw)
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("runner: 网关端口非法: %q", port)
		}
		return n, nil
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return 80, nil
	case "https":
		return 443, nil
	default:
		return 0, fmt.Errorf("runner: 网关地址缺少端口且 scheme 未知: %q", raw)
	}
}

// runTools 按普通 run 已就绪的能力追加 bash 与生图工具;compact 两者都不传。
func (r *Runner) runTools(executorReady, imageGeneration bool) string {
	tools := r.tools
	if executorReady {
		tools += ",bash"
	}
	if imageGeneration {
		tools += ",image_generation"
	}
	return tools
}
