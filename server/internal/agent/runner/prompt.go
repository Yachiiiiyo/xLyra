package runner

import (
	"fmt"
	"strings"
	"time"
)

// defaultSystemPrompt 是 xLyra Agent 的默认系统提示,经 --system-prompt <文件>
// 传给 Pi。07 §二 B 实测它只替换默认前缀(身份句、工具说明),技能列表与 AGENTS.md
// 仍会保留,这正是我们要的(不用扩展的 before_agent_start 整段替换)。
// Config.SystemPrompt 可整体覆盖,为空时用本默认(P4 接设置)。
//
// 措辞注意(真实冒烟发现):不能写成"修改文件前会请用户确认"这类让模型自己去问的话,
// 否则部分模型会在聊天里先问一遍而不调用工具,真正的审批卡片就出不来。
const defaultSystemPrompt = `你是 xLyra Agent,运行在 xLyra 的受控环境中。

遵守以下规则:
- 默认使用用户的语言回答。
- 当前目录是本次会话的工作目录,只能访问其中的文件。
- 可用工具仅限读取、列目录、搜索、查找、写入与编辑文件。
- 需要修改文件时,直接调用写入或编辑工具。系统会自动向用户弹出确认,不要在对话里另外询问"是否允许"。
- 如果工具返回"用户拒绝了本次文件修改",不要重复尝试同一操作,改为简短说明并询问用户的意图。
- 不要编造文件内容:先读文件再修改,基于真实内容回答。
- 保持回答简洁,直接解决用户的问题。`

const bashSystemPrompt = `

沙箱命令执行规则:
- 命令在隔离沙箱中执行,没有网络;只能读写当前工作区和私有临时目录,系统目录只读。
- 每条命令需要用户确认时,系统会自动弹出确认。
- 单条命令有超时限制,默认 %d 秒。
- 命令结束后所有后台进程都会被清理,不要依赖常驻进程。
- 需要联网或安装依赖的需求无法完成,应如实告诉用户。`

const imageGenerationSystemPrompt = `

可以使用 image_generation 工具生成图片,图片会保存在工作区 images/ 下,图片内容不会返回给你。
生成成功后,请在最终回复中用工具结果给出的 Markdown(![说明](images/文件名))原样展示图片,可放在文字说明之间,不要改动路径。
每个会话轮次的生图调用次数有限,请先想清楚再调用。`

// systemPrompt 返回本次 run 使用的系统提示文本。
func (r *Runner) systemPrompt(bash bool, defaultTimeout time.Duration, imageGeneration bool) string {
	text := strings.TrimSpace(r.cfg.SystemPrompt)
	if text == "" {
		text = defaultSystemPrompt
	}
	if bash {
		text += fmt.Sprintf(bashSystemPrompt, int(defaultTimeout/time.Second))
	}
	if imageGeneration {
		text += imageGenerationSystemPrompt
	}
	return text
}

// permissionModeValue 归一化写进 xlyra.json 的权限模式:只有 full 视为放行,
// 其余(含空、非法值)一律 ask,由扩展按 ask 走审批。
func permissionModeValue(mode string) string {
	if mode == "full" {
		return "full"
	}
	return "ask"
}
