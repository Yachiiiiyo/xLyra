package sandbox

// Landlock 访问位常量,数值与内核 uapi/linux/landlock.h、x/sys v0.48 的
// LANDLOCK_ACCESS_FS_*/LANDLOCK_ACCESS_NET_*/LANDLOCK_SCOPE_* 一致(后者仅
// linux 构建可见,landlock_linux.go 有编译期对照)。fsAccessForABI 等是
// 纯函数、要在 macOS 上单测,故本地定义。
const (
	accessFSExecute   uint64 = 0x1
	accessFSWriteFile uint64 = 0x2
	accessFSReadFile  uint64 = 0x4
	accessFSReadDir   uint64 = 0x8
	accessFSRemoveDir uint64 = 0x10
	accessFSRemoveFil uint64 = 0x20
	accessFSMakeChar  uint64 = 0x40
	accessFSMakeDir   uint64 = 0x80
	accessFSMakeReg   uint64 = 0x100
	accessFSMakeSock  uint64 = 0x200
	accessFSMakeFIFO  uint64 = 0x400
	accessFSMakeBlock uint64 = 0x800
	accessFSMakeSym   uint64 = 0x1000
	accessFSRefer     uint64 = 0x2000 // ABI≥2
	accessFSTruncate  uint64 = 0x4000 // ABI≥3
	accessFSIoctlDev  uint64 = 0x8000 // ABI≥5
	// LANDLOCK_ACCESS_FS_RESOLVE_UNIX(内核 uapi linux/landlock.h,ABI≥9);
	// x/sys v0.48 尚未定义。纳入 handled 后,connect 文件系统 Unix 套接字也受路径规则约束。
	accessFSResolveUnix uint64 = 1 << 16 // ABI≥9

	accessNetBindTCP    uint64 = 0x1 // ABI≥4
	accessNetConnectTCP uint64 = 0x2

	scopeAbstractUnixSocket uint64 = 0x1 // ABI≥6
	scopeSignal             uint64 = 0x2
)

// fsAccessForABI 返回内核 ABI 已知的全部文件系统访问位:没放进
// handled_access_fs 的权限不受限制,因此必须按 ABI 填全。对 ABI 1..10
// 单调不减;ABI≥9 额外处理文件系统 Unix 套接字 connect。
func fsAccessForABI(abi int) uint64 {
	access := accessFSExecute | accessFSWriteFile | accessFSReadFile | accessFSReadDir |
		accessFSRemoveDir | accessFSRemoveFil | accessFSMakeChar | accessFSMakeDir |
		accessFSMakeReg | accessFSMakeSock | accessFSMakeFIFO | accessFSMakeBlock |
		accessFSMakeSym
	if abi >= 2 {
		access |= accessFSRefer
	}
	if abi >= 3 {
		access |= accessFSTruncate
	}
	if abi >= 5 {
		access |= accessFSIoctlDev
	}
	if abi >= 9 {
		access |= accessFSResolveUnix
	}
	return access
}

// roDirAccess 是只读目录的访问位(可执行、可读文件、可列目录)。
func roDirAccess() uint64 {
	return accessFSExecute | accessFSReadFile | accessFSReadDir
}

// roFileAccess 是只读普通文件的访问位:READ_DIR 等目录位给文件会 EINVAL,
// 文件只给文件适用的位。
func roFileAccess() uint64 {
	return accessFSExecute | accessFSReadFile
}

// devReadAccess/devWriteAccess 是设备文件的访问位:只按文件放行、不放开
// 整个 /dev(否则 /dev/mem 也能开);ABI≥5 起 ioctl 需要 IOCTL_DEV 位。
func devReadAccess(abi int) uint64 {
	a := accessFSReadFile
	if abi >= 5 {
		a |= accessFSIoctlDev
	}
	return a
}

func devWriteAccess(abi int) uint64 {
	return devReadAccess(abi) | accessFSWriteFile
}
