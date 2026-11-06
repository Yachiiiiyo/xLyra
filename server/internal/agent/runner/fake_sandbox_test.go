package runner

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// runSandboxProbes 是容器集成测试里的假 Pi 探针,结果落在工作区供 root 断言。
func (p *fakePi) runSandboxProbes() {
	result := map[string]any{"uid": os.Getuid(), "gid": os.Getgid()}
	_, err := os.ReadFile(p.sandboxSecret)
	result["secret_denied"] = err != nil
	result["secret_error"] = errorString(err)
	result["outside_denied"] = os.WriteFile(p.sandboxOutside, []byte("no"), 0o600) != nil
	workspaceFile := filepath.Join(p.sandboxResult + ".probe")
	result["workspace_write"] = os.WriteFile(workspaceFile, []byte("ok"), 0o600) == nil
	data, err := os.ReadFile(workspaceFile)
	result["workspace_read"] = err == nil && string(data) == "ok"
	_, err = os.ReadFile(fmt.Sprintf("/proc/%d/environ", os.Getppid()))
	result["parent_environ_denied"] = err != nil
	result["gateway_ok"] = tcpReachable(p.sandboxGateway)
	result["other_denied"] = !tcpReachable(p.sandboxOther)
	if p.sandboxStarted != "" {
		_ = os.WriteFile(p.sandboxStarted, []byte("started"), 0o600)
	}
	rawConfig := os.Getenv("XLYRA_RUN_CONFIG")
	if rawConfig == "" {
		result["config_error"] = "XLYRA_RUN_CONFIG is empty"
	} else {
		var parsed struct {
			Runtime struct {
				Bash struct {
					Socket string `json:"socket"`
				} `json:"bash"`
			} `json:"runtime"`
		}
		if err := json.Unmarshal([]byte(rawConfig), &parsed); err != nil {
			result["config_error"] = err.Error()
		} else {
			stdout, stderr, code, err := fakeExecRequest(parsed.Runtime.Bash.Socket, p.sandboxCommand)
			result["exec_stdout"] = stdout
			result["exec_stderr"] = stderr
			result["exec_code"] = code
			result["exec_error"] = errorString(err)
			if uid, parseErr := strconv.Atoi(lastLine(stdout)); parseErr == nil {
				result["exec_uid"] = uid
			}
		}
	}
	encoded, _ := json.Marshal(result)
	_ = os.WriteFile(p.sandboxResult, encoded, 0o600)
}

func tcpReachable(address string) bool {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func fakeExecRequest(socket, command string) (string, string, int, error) {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return "", "", -1, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"cmd": command}); err != nil {
		return "", "", -1, err
	}
	var stdout, stderr string
	code := -1
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var msg struct {
			T    string `json:"t"`
			D    string `json:"d"`
			Code int    `json:"code"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return stdout, stderr, code, err
		}
		switch msg.T {
		case "out", "err":
			data, err := base64.StdEncoding.DecodeString(msg.D)
			if err != nil {
				return stdout, stderr, code, err
			}
			if msg.T == "out" {
				stdout += string(data)
			} else {
				stderr += string(data)
			}
		case "exit":
			return stdout, stderr, msg.Code, nil
		}
	}
	return stdout, stderr, code, scanner.Err()
}

func lastLine(text string) string {
	for len(text) > 0 && (text[len(text)-1] == '\n' || text[len(text)-1] == '\r') {
		text = text[:len(text)-1]
	}
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '\n' {
			return text[i+1:]
		}
	}
	return text
}
