// Package sandbox 实现命令与 Pi 进程的 Linux 沙箱(08-implementation-plan
// §十六,方案 A):身份降权 + Landlock 文件系统/网络 + seccomp 系统调用
// 过滤 + rlimit 资源限制 + 按 uid 清理残留进程。方案与踩坑结论来自 PoC
// (docs/pi-agent-integration/07-poc-results.md §十二、§十三),实现保持
// PoC 验证过的系统调用写法,不引入第三方 Landlock/seccomp 库。
//
// 架构:helper 由 xLyra 主程序(或测试二进制)重新执行自己充当——
// `<exe> __xlyra-sandbox <mode> --spec <base64(JSON)> -- argv...`。main 与
// TestMain 的第一件事是调用 MaybeRunHelper,参数不匹配立即返回 false 走
// 正常启动;匹配则在 helper 内部 os.Exit。两种模式:
//
//   - child:已在目标 uid 下运行。LockOSThread → no_new_privs → Landlock →
//     seccomp(TSYNC)→ rlimit → umask/chdir → exec 目标程序;环境只用
//     Spec.Env,不继承。任一步失败退出码 2,绝不降级继续。
//   - run:以 root 运行,负责命令沙箱。设 CHILD_SUBREAPER,以目标凭据 +
//     Setpgid + Pdeathsig 启动 child 并转发 stdio;超时杀整组退出码 124,
//     SIGTERM/SIGINT 退出码 128+n;结束后按 uid 清理并断言零残留,清理
//     失败退出码 125。
//   - probe:无 --spec,由 child 在沙箱内 exec,按 XLYRA_PROBE_* 环境变量
//     做只读/受控写入探测,stdout 返回 JSON 供执行器启动自检使用。
//
// Spec 经 base64(JSON) 放进 argv——argv 对同机其他 uid 可见,因此 Spec 里
// 不得含任何密钥;完整环境只经 Spec.Env 传递。
//
// 清理分两版:helper 进程内(run 模式)是 subreaper 小进程,可用 wait4(-1)
// 收尸;导出给主进程的 KillUID/SweepUIDs 绝不 wait4(-1)(会偷走主进程其他
// 子进程的收尸),只发 SIGKILL,僵尸交给 init/父进程。
//
// RLIMIT_NPROC 按 uid 计数,只有配合 isolation 包的每会话独立 uid 才有意义。
// 非 Linux 平台本包全部为 stub,Detect 返回不可用。
package sandbox
