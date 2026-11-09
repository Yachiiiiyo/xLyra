# Changelog

## [1.18.0](https://github.com/Yachiiiiyo/xLyra/compare/v1.17.0...v1.18.0) (2026-11-09)


### Features

* ✨ 「替我审批」权限级别：新增 auto 权限（询问与完全访问之间），审批由分类器模型代为判定且不弹卡片；分层提示词（硬拦截/软拦截/默认放行/用户意图），模型报错最多重试 2 次、失败即拒绝；扩展识别 auto（write/edit 直接放行，bash 命中高危规则由 runner 确定性拒绝）；连续被拒 3 次或累计 20 次中止运行；审批卡片展示自动允许/拒绝及理由；权限按钮改为三档菜单 ([2485991](https://github.com/Yachiiiiyo/xLyra/commit/2485991664bb843a6623246f9041d33095267cf1))
* ✨ Agent 全局共享工作区：所有会话共用同一个 workspace（默认 &lt;数据目录&gt;/workspace，可用 AGENT_WORKSPACE_DIR 覆盖），分叉会话直接共享文件；旧会话非空 workspace 启动时迁入 _legacy/&lt;会话ID&gt;；系统提示环境段注入工作区绝对路径；共享目录不计入会话配额；uid 隔离模式下工作区 0777 且 umask 为 0；分叉响应去掉 workspace_copied ([9aa17f5](https://github.com/Yachiiiiyo/xLyra/commit/9aa17f57c4d4dee42be240039920b0329a779653))
* ✨ Agent 前端支持生成图片：消息内渲染生成的图片，设置里新增图片模型选择，可用模型配置拆为独立弹窗 ([bfee874](https://github.com/Yachiiiiyo/xLyra/commit/bfee874df16bd1130ae9958a05d2896e376783af))
* ✨ Agent 前端改用主程序设计并收口内置引擎：删除 Runner 专属功能、范围并入设置弹窗、新增运行环境面板与不可用提示，LiquidGlass 与 Dialog 统一风格，修复取消/错误/审批状态、bash 审批提交与审批等待期间的计时和重复卡片 ([eb49f75](https://github.com/Yachiiiiyo/xLyra/commit/eb49f75a46919da2f10d367fd7905e67cd32ec06))
* ✨ Agent 前端新增分叉会话（会话菜单与回复操作行）与引用其他会话（输入区选择、引用标签与回放） ([b56bd2f](https://github.com/Yachiiiiyo/xLyra/commit/b56bd2f25118d4871123644294f26e1561f45b75))
* ✨ Agent 命令沙箱改为可选（默认关闭）：Agent 设置「环境」面板新增沙箱开关（能力配置 sandbox_enabled，重启后生效）；未启用时 Pi 与命令直接在本机运行、可联网，bash 开放并沿用 ask/auto/full 审批与高危规则，扩展新增本机 bash 模式（复用 Pi 本地 shell + 超时包装）；启用时行为不变；required 仍强制沙箱；系统提示按模式区分命令执行规则；环境面板文案同步 ([3d037f7](https://github.com/Yachiiiiyo/xLyra/commit/3d037f7474fcdf39ab880a9323eaebe633db87d6))
* ✨ Agent 回复底部复制图标右侧显示时间（12 小时内相对时间，之后具体时间），会话菜单新增复制会话 ID ([9a0b647](https://github.com/Yachiiiiyo/xLyra/commit/9a0b647f79c9735b407c8867a06b7cc7dce031e6))
* ✨ Agent 回复底部新增「本轮变更」区：列出本轮 write/edit 改动的文件与增删行数，点击在右侧面板（窄屏为底部抽屉）查看 diff 与源码；后端在转录与实时事件里透传 edit 工具的 patch，运行中不显示变更区，同一文件先 write 后 edit 时按合并后的整份内容展示 ([f3c91e6](https://github.com/Yachiiiiyo/xLyra/commit/f3c91e63205845f1c79e2ba5909b2d2924b55d05))
* ✨ Agent 导航图标改为 Sparkles，请求详情代码块改用玻璃填充底色，调整背景遮罩样式 ([cb5c040](https://github.com/Yachiiiiyo/xLyra/commit/cb5c040fb7473b2c9d007570fef5791d9db49787))
* ✨ Agent 提问工具与提问卡片：模型可调用 question 工具让用户选择（单选/多选/其他补充），前端按分步向导逐题回答、确认后提交；新增回答接口、超时自动跳过（AGENT_QUESTION_TIMEOUT）、等待审批/提问时暂停运行总时限，回答摘要显示在回复区，会话记录/引用/分叉支持提问条目 ([3341574](https://github.com/Yachiiiiyo/xLyra/commit/3341574e7e70d1cf4ca08c49f98e28dac3dde062))
* ✨ Agent 文件预览：读取接口放宽到工作区多级路径（文本一律 text/plain、逐级拒绝链接、新增下载与大小限制），面板新增预览标签（Markdown、沙箱 HTML、图片、SVG、CSV、文本）与下载，回复里的相对路径链接可直接打开预览 ([63f3be8](https://github.com/Yachiiiiyo/xLyra/commit/63f3be8144a227dccfce4ae5bf8d4561357e4379))
* ✨ Agent 本轮变更覆盖 bash 等所有写文件途径：每轮前后对工作区做清单求差（含内容寻址的修改前快照与按需 diff），新增 changes/diff/artifacts/artifacts.zip 接口，变更区改以后端 run_changes 为准；输入框上方新增「文件 · N」入口与会话文件列表（点文件名展开原文、展开行吸附顶部、变更文件默认打开预览），预览面板复制改为复制内容 ([1f3501b](https://github.com/Yachiiiiyo/xLyra/commit/1f3501b0807298e2f31e4c1521ed3c51fb7c01e3))
* ✨ Agent 权限卡片三档（允许、会话内允许、拒绝），可用模型概览仅保留查看入口，自动放行显示与会话菜单撤销自动允许 ([d731650](https://github.com/Yachiiiiyo/xLyra/commit/d73165006e02a3db4418f36244214590fb21a457))
* ✨ Agent 生成图片的放大查看改为只放大图片：深色遮罩、无边框底板，下方仅保留下载按钮 ([1aefd5c](https://github.com/Yachiiiiyo/xLyra/commit/1aefd5c94652a820194a13c38e1b65ef7cfd08a8))
* ✨ Agent 设置弹窗改版：范围改为可用模型并置顶概览与查看弹窗，Skills 新增添加与创建入口，移除人格配置 ([9404d58](https://github.com/Yachiiiiyo/xLyra/commit/9404d58d0886e67d2b136530e7d84a2c5a5e0e4e))
* ✨ Agent 输入区对齐 Playground：输入框加高、附件改用 AttachmentMenu、权限按钮统一触发器样式 ([9122d7a](https://github.com/Yachiiiiyo/xLyra/commit/9122d7a78b1be89f328836aaeba7679261e6a213))
* ✨ Agent 页适配自定义背景：有背景图时侧栏、输入框与会话遮罩改用玻璃面板，无背景图时保持星点背景，设置弹窗与引用弹窗样式同步调整 ([6cb816b](https://github.com/Yachiiiiyo/xLyra/commit/6cb816b4055f63b561ab8bdd3ac20c982085043b))
* ✨ 内置 Agent 接入 /api/v1/agent 契约：新增 AGENT_ENGINE（默认 pi）、沙箱与隔离配置、健康状态、会话/分叉/授权接口与启动自检，runner 引擎保持转发 ([b09e8b4](https://github.com/Yachiiiiyo/xLyra/commit/b09e8b4e75eaeaf0cd67808fca4cb23b51b903b6))
* ✨ 内置 Agent 新增 Pi RPC 进程客户端、Pi 事件到前端事件的翻译器与 run 事件缓冲（SSE 输出、断线重放），固化 Pi 事件样本为测试夹具 ([c48f389](https://github.com/Yachiiiiyo/xLyra/commit/c48f389152c2f433f9aa04712fc20929a88a09c8))
* ✨ 内置 Agent 新增 Pi 运行配置生成（模型、思考档位、Claude 5 系列强制 adaptive 思考）与进程内 run 令牌签发 ([77d9edc](https://github.com/Yachiiiiyo/xLyra/commit/77d9edca3edc919ee0e66e6f544e59b93fec42f3))
* ✨ 内置 Agent 新增会话存储与 transcript 构建，支持分叉会话与引用其他会话上下文，审批与失败记录按 Pi 会话文件锚点定位 ([b1340aa](https://github.com/Yachiiiiyo/xLyra/commit/b1340aacb86811eb6a3ca1266130fb7c33e9a9f1))
* ✨ 内置 Agent 新增文生图工具：扩展提供 generate_image，经进程内令牌调用网关生成，图片落盘到会话目录并在 transcript 与文件接口中回放，新增图片模型配置与可用图片模型列表 ([0cbf43e](https://github.com/Yachiiiiyo/xLyra/commit/0cbf43e0065ee2f08f97703e65899a062eae2389))
* ✨ 内置 Agent 新增能力存储：Skills、AGENTS.md 与上下文配置的磁盘存取与校验 ([194c3cf](https://github.com/Yachiiiiyo/xLyra/commit/194c3cfd7dff75813207da8ebe0be582784dccf9))
* ✨ 内置 Agent 新增运行编排器：Pi 进程生命周期、令牌、审批、会话内允许、停止、重试、手动压缩、并发排队与配额、沙箱 bash、分叉与引用 ([cc8915d](https://github.com/Yachiiiiyo/xLyra/commit/cc8915ddd1136d1e8a466be178fc84f1f387c4c1))
* ✨ 删除模型体验页：移除路由、导航入口、三语文案与 /api/playground 代理，附件菜单与推理设置选择器迁到 components/chat，旧历史数据与浏览器本地存储保持不动 ([dc7fc9f](https://github.com/Yachiiiiyo/xLyra/commit/dc7fc9faeb7ce3cf6a766c1f3aa1a35aae6992d6))
* ✨ 备份按内容类别导出与恢复：新增 9 个可选类别（系统设置、站点与模型、API 密钥与权限、请求日志与用量、插件、Agent 设置/能力文件/会话记录/工作区），类别间硬依赖自动联动、软依赖提示；格式升到 v4（旧 v2/v3 备份继续可导入，忽略已废弃的 Playground）；备份纳入此前遗漏的插件表与模型端点覆盖表；恢复按类别替换并清空依赖数据（汇总表随明细清空，悬空的插件输入引用会被清理）；恢复前新增预览（只解密备份开头一块，列出将恢复/将清空/无法恢复的内容）；恢复期间拦住运行中的 Agent 并在恢复后重载插件；定时备份补上 Agent 目录（此前定时备份不含 Agent 数据）；自动备份可配置备份内容；前端以按钮加弹窗选择备份内容，并重做恢复预览与进度对话框；移除 agent.runner_* 旧配置的导出；沙箱开启时恢复后放开共享工作区权限 ([d45dd06](https://github.com/Yachiiiiyo/xLyra/commit/d45dd0601cb5ceaae6519d1cfe6efa9df42eeb16))
* ✨ 外观设置新增菜单设置：可自定义显示的菜单项与程序首页（仪表盘 / Agent） ([ec3d72b](https://github.com/Yachiiiiyo/xLyra/commit/ec3d72bfda33fd261d6f946b468302a461706daa))
* ✨ 审批模型配置：Agent 设置新增独立的「审批模型」（agent.classifier_model，未配置回退当前对话模型，不回退快速模型），审批模型与快速模型改为行内下拉选择并各自自愈清理失效配置，移除快速模型弹窗 ([9078dae](https://github.com/Yachiiiiyo/xLyra/commit/9078dae63eeb31d3a8367d94531c63bb8154a667))
* ✨ 快速模型扩展用途：新增 summarize_file 文件摘要工具（扩展经本 run 令牌调用 fast-task 端点，由快速模型提炼文件前 256 KiB，只把摘要回给主模型，免审批、走路径约束、每 run 限 40 次）与 AI 会话标题（首条消息后异步生成，失败保持截断标题，生成后经 session_title 事件即时更新侧栏）；快速模型未配置时均回退当前对话模型 ([fee4848](https://github.com/Yachiiiiyo/xLyra/commit/fee484813ecc6ebccde443186debd000b83c7e28))
* ✨ 抽屉的返回与关闭按钮、代码块复制按钮和表格表头改为透明磨砂样式，适配背景图 ([20839c1](https://github.com/Yachiiiiyo/xLyra/commit/20839c1d29793cd0778d15d67c9dee868aadf485))
* ✨ 抽屉默认支持上滑扩展到全屏，不再要求内容区可滚动 ([626f02f](https://github.com/Yachiiiiyo/xLyra/commit/626f02fd35070fc60af8ca7304906c9bceb4d0a8))
* ✨ 支持自定义实例背景：新增外观设置页，整页背景图层与可读性遮罩，主侧栏在有背景图时改用折射玻璃 ([db8cb42](https://github.com/Yachiiiiyo/xLyra/commit/db8cb426e168f56722f924c8c1c0804664cb9fa6))
* ✨ 新增 Agent 进程隔离：每会话专属 uid 槽位、目录属主与权限、探测与带正向对照的启动自检 ([08b96ae](https://github.com/Yachiiiiyo/xLyra/commit/08b96aed0f2085753351141f2753fc77968abe27))
* ✨ 新增 agent-runtime：锁定 Pi 1.4.0，提供 xLyra 扩展（路径约束、write/edit/bash 审批、沙箱 bash 执行器协议） ([989e26b](https://github.com/Yachiiiiyo/xLyra/commit/989e26b7b6d0ed0dc52b3898a0aa30c78a8c91d5))
* ✨ 新增命令沙箱与执行器：Landlock、seccomp、资源限制、降权 helper，Unix 套接字协议串行执行并按 uid 清理零残留 ([d7810fc](https://github.com/Yachiiiiyo/xLyra/commit/d7810fca6b91299a9e0cf72aab8028628397dcc5))
* ✨ 新增外观设置接口：读取与更新实例背景图配置，上传的自定义背景按内容哈希落盘并提供读取 ([0e905f4](https://github.com/Yachiiiiyo/xLyra/commit/0e905f41bc6ee29e1b80fc3aaa83fa173b3ac52c))
* ✨ 站点/模型选择器重做为顶部胶囊 Tabs + 下方模型列表，桌面与移动端统一；生图模型配置复用同一组件，Tabs 滑块跟随标签宽度变化，去掉「显示停用站点」选项 ([cb92051](https://github.com/Yachiiiiyo/xLyra/commit/cb92051a53978da29f4b535d097ca33c8da50dbb))
* ✨ 系统备份纳入 Agent 能力文件：备份包新增可选的 agent_capabilities 段（skills/、AGENTS.md、config.json），手动与自动备份均包含；恢复时先解压到临时目录再整体切换并支持回滚，拒绝越界路径与符号链接，限制单文件 64MB/总量 512MB，权限与能力包一致；旧备份（无 sections）导入不动现有 Agent 文件；导入结果新增 agent_files，进度新增 agent 步骤 ([19a9a2b](https://github.com/Yachiiiiyo/xLyra/commit/19a9a2b1c1b435f4f554c42900c3e5baf82a23e0))
* ✨ 网关新增文生图执行器：按候选模型与站点依次故障转移，记录每次尝试的失败原因，故障转移上限不再按配置预分配容量 ([6af8c0d](https://github.com/Yachiiiiyo/xLyra/commit/6af8c0db1a373628ab3ed15d864fa86ee9f5fbd1))
* ✨ 聊天通用组件与工具迁出 playground 到 components/chat 与 lib，Agent 页改用 chat 命名空间，样式类与属性去掉 playground 前缀 ([c74b764](https://github.com/Yachiiiiyo/xLyra/commit/c74b7647fbb7bebd9b6b4fe64a554e7bf63584e4))
* ✨ 输入框下方会话统计行：新增 GET /sessions/{id}/stats（轮/步、累计 token、总输入输出、缓存命中/写入、命中率、上下文占用，读 Pi 会话文件仅统计当前分支，按文件大小+修改时间缓存）与 runner 实测生成速度 tok/s（首个 delta 到消息结束，排除首字等待与网关重试，每条消息结束即写入样本）；前端桌面左中右三栏、移动端仅 tok/s/缓存命中/圆环，对话界面行高固定占位避免输入框跳动；Token 用量复用请求页弹窗、上下文窗口浮窗（桌面悬停/移动端点击）；运行中随每一步（tool_call/tool_result 节流）刷新，终态与压缩完成立即刷新 ([5ec4827](https://github.com/Yachiiiiyo/xLyra/commit/5ec48276918f611ffc45fd991bbf6d6749465906))
* ✨ 输入预测（Ghost Text）与快速模型配置：每轮对话结束后预测用户下一条消息，空输入框显示灰字，按 Tab / → 或点击提示接受，输入后删空会重新显示；新增预测接口（等待 run 收尾、过滤器、上下文裁剪）与进程内文本补全；Agent 设置新增「快速模型」（未配置时回退当前对话模型并提示，可用范围变化时连带清除）与「输入预测」开关；run 开始/结束与预测完成日志降为 debug ([5491cd7](https://github.com/Yachiiiiyo/xLyra/commit/5491cd7118acb06ab911528f981890cb1a6c3ab5))
* ✨ 重写 Agent 默认系统提示：分运行方式/工具使用/工作方式/回复风格四块，补充并行调用、工作循环、报错不重发、文件状态时效与友好语气；系统提示末尾追加当前日期环境段，bash 段新增改后跑测试指引 ([270f17d](https://github.com/Yachiiiiyo/xLyra/commit/270f17d32e0af036d8fc5e39a5929e4576b1d540))


### Bug Fixes

* 🐛 Agent 回复里不再渲染模型编造的 images/ 图片引用，生图提示去掉可照抄的 Markdown 示例；已处理的权限请求收成一行；撤销自动允许文案精简 ([0a06e7c](https://github.com/Yachiiiiyo/xLyra/commit/0a06e7cd3cfddced7a60222c029b1d5bbb87ddf1))
* 🐛 Agent 提问默认一直等待用户回答，同时运行的 Agent 上限从 4 个提高到 16 个 ([cd2b86f](https://github.com/Yachiiiiyo/xLyra/commit/cd2b86f0e76b7ca1b35c9fb195f16dbf29b3f689))
* 🐛 Agent 输入框：引用会话并入「+」菜单，窄屏下模型选择器可收缩不再把发送按钮挤出，新建对话输入框的小螃蟹不再被裁切 ([13ffe8e](https://github.com/Yachiiiiyo/xLyra/commit/13ffe8ec4e664341d16a0755c6f792f133a118b0))
* 🐛 Agent 页打开默认停在无对话初始页，不再恢复上次会话；移动端对话顶部留白按安全区计算，首条消息不再被顶栏遮挡 ([49e1ad7](https://github.com/Yachiiiiyo/xLyra/commit/49e1ad79081d39957b939741f6805658404574ea))
* 🐛 修复合并前 CI：升级 brotli/compress 消除 govulncheck 漏洞，扩展拒绝路径不再泄露真实路径，沙箱集成测试与镜像冒烟显式开启沙箱，隔离自检环境变量对照兼容非 dumpable 进程 ([73f3d0a](https://github.com/Yachiiiiyo/xLyra/commit/73f3d0ab70bd5a6528fece5097a29e7bf362d67e))
* 🐛 完全访问不再对高危命令二次确认：full 模式 bash 校验通过即直接放行，高危规则仅供「替我审批」做确定性拒绝；修复「下载脚本直接执行」规则对 wget|node -e、curl|python -c、curl|bash -c 等内联脚本管道的误报 ([08073bd](https://github.com/Yachiiiiyo/xLyra/commit/08073bd41055afa9d0bce2f289364c9c3d03a6ae))
* 🐛 沙箱集成测试不再继承宿主 PATH，并让 Pi 身份能遍历 CI 里的 Pi 入口目录 ([5f06c16](https://github.com/Yachiiiiyo/xLyra/commit/5f06c1669eeeb26bf76bcb287d1aea38eb6a4e9c))
* 🐛 程序首页设为 Agent 后左上角返回失效：返回时有应用内上一页则回退，否则默认替换跳转到仪表盘 ([49317c8](https://github.com/Yachiiiiyo/xLyra/commit/49317c8bc5e00f4fe28643ca4ff400dec0dfaa66))
* 🐛 网关 Anthropic 协议输出的 input_tokens 改为不含缓存读写（官方语义）：此前输出含缓存的总提示 token，与 cache_read_input_tokens 重复计数，导致下游（如 Pi）的缓存命中率偏低、累计 token 与上下文占用偏大、自动压缩触发偏早；内部记录与计费口径、OpenAI 协议输出不变，已写入的旧会话数据不迁移 ([2139808](https://github.com/Yachiiiiyo/xLyra/commit/21398082fe85af22e714d717ae79fe2147e3f927))
* 🐛 网关流式转发推理增量时原样保留空白，修复思考文本词间空格丢失 ([235b3d2](https://github.com/Yachiiiiyo/xLyra/commit/235b3d204512c2cd1ff753904f573f4b51b74589))
* 🐛 表格表头样式收口到全局组件：模型价格页等手写的不透明深色表头改用 DataTableHeader 与统一的 fill 底色，导出统一的表头文字与 grid 表头样式 ([176dbca](https://github.com/Yachiiiiyo/xLyra/commit/176dbcab5fa6e03910b5e102c23b95cdc3a77720))


### Performance Improvements

* ⚡ Agent 配置取数改用批量接口：站点列表新增 lite 参数跳过用量聚合与账号计数，新增 /site-models 一次返回全部站点模型，前端去掉逐站点请求；可用模型页去掉边框改用分隔线、生图摘要不再显示站点名 ([cf15867](https://github.com/Yachiiiiyo/xLyra/commit/cf1586713003157ec5db9a652cfcce500c8495be))
* ⚡ 前端加载提速：背景图 WebP 转码/预加载/淡入/SW 缓存，Agent 路由并行预取与查询快照 ([c000cc1](https://github.com/Yachiiiiyo/xLyra/commit/c000cc14e76d42b0cc823d61bbfcfed39f2c3cfa))
* ⚡ 后端静态资源 br/gzip 压缩与 Agent 会话列表缓存 ([2f9d03c](https://github.com/Yachiiiiyo/xLyra/commit/2f9d03ceaa3198c4b62196e905c31fc6014080e6))

## [1.17.0](https://github.com/Yachiiiiyo/xLyra/compare/v1.16.0...v1.17.0) (2026-11-02)

本版本新增「插件市场」与「xLyra Cloud」：实例可以绑定 xLyra Cloud、从市场安装和更新插件，并一键提交自己开发的插件；同时补强了网关流式输出的稳定性，并优化了移动端与启动体验。

### Features

#### 插件市场
* ✨ 引入插件市场协议 v1 校验包与测试向量；新增插件市场与实例绑定的数据表、仓储与设置项
* ✨ 市场后端：目录同步、图标中转、安装、marketplace 信任、更新提示、自动升级失败回退与吊销执行；启动时立即同步，避免刚重启时因未缓存 root 而连接失败
* ✨ 插件顶层 `icon`：深色背景可读性检查、图标接口泛化，`xlyra-plugin` 支持同步检查与打包
* ✨ 管理接口：插件市场、实例绑定、开发者身份与一键提交；已安装列表补充图标、归属、领先判定与信任状态
* ✨ `xlyra-plugin publish`：用 API 令牌提交已签名插件包并轮询审核状态，补充命令行与发布文档

#### xLyra Cloud
* ✨ 实例绑定客户端：配对轮询、签名心跳与上报、开发者身份与提交转发
* ✨ Hub 统一改名为 xLyra Cloud：`cloudclient` 包、`/cloud-binding` 接口、`cloud_binding` 表、`xlyra-plugin --cloud` 参数与发布文档，前端接口、字段、标识符与三语文案同步
* ✨ Cloud 错误状态码改为 424，避免被 Cloudflare 替换成 HTML 页面

#### 设置与前端
* ✨ 设置新增「系统」分组与 xLyra Cloud 页，插件页迁到 `/system/plugins`，含配对弹窗、连接状态与开发者身份
* ✨ 插件页：市场列表与详情、安装与更新提示、插件图标、一键提交到市场；已安装改为卡片式、标签换胶囊、受信公钥删除二次确认；去掉市场设置及前后端接口（配置内置默认），「已上传」文案改为「已安装」并补充市场安装说明
* ✨ xLyra Cloud 页重构为横条实例卡、开发者身份双栏与独立危险区
* ✨ 前端通用组件：SplitButton、共享 Notice、InputOTP；备份页提示块改用 Notice

#### 启动与状态页
* ✨ 新增全局启动遮罩（星空背景 + 中央呼吸 logo，最短展示 1s，鉴权与首屏路由就绪后淡出），移除开屏 Loading 文案
* ✨ 404 与 502 改为全屏独立状态页（不带侧边栏与顶栏，登录页同款玻璃卡片），404 增加返回首页按钮，502 增加刷新页面按钮

#### 移动端
* ✨ 抽屉支持上滑长高、下拉缩回、下拉关闭（保存类抽屉禁止下拉关闭）；更多菜单限高可滚动；调整顶栏用户区、模型测试抽屉与路由详情布局

### Bug Fixes

* 🐛 网关流式出口新增 SSE 规整器：补全 response 信封（`created_at` 等）、转义控制字符、丢弃无法修复的事件、断流时合成 `response.failed`，并在请求日志记录 `stream_sanitized`
* 🐛 移动端更多菜单顶部留足空间，避免最上面一项旋转后被容器上边裁掉
* 🐛 账号池额度测试改用当前时间，不再依赖固定日期

## [1.16.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.15.2...v1.16.0) (2026-10-24)

本版本是一次大更新：新增「站点类型插件」与「插件账号池」，并把前端整体重构为新的深色视觉（Apple HIG + Liquid Glass）。

### Features

#### 插件：站点类型（site_provider）
* ✨ 新增插件类型 site_provider（API Key 站点类型）：manifest 校验、必须受信签名、启用时确认主机列表、CLI 脚手架
* ✨ 站点类型注册表支持合并插件站点类型（ID 格式校验、原子快照）；已启用插件在目录重载后自动接入站点类型、协议覆盖表与 adapter 动态模块，并在多实例间数秒内同步
* ✨ 钩子：`listModels` / `check` / `probe` / `classify` / `rewritePayload`，请求头模板（`request.headers` / `stripDownstreamHeaders`），`quota.aggregate`（sum/min/none）；fixtures 按 hook 自检，`xlyra-plugin run --hook`
* ✨ 生命周期与安全：停用后不参与路由与下游模型目录；站点类型 ID 锁定、卸载保护；出站主机白名单（创建/更新、通用适配器、网关转发含重定向均按精确主机名校验）
* ✨ 插件图标服务 `GET /api/v1/plugin-icons/{pluginID}`；插件详情按当前版本展示站点提供方并标注版本状态；近 24 小时统计改为彩色圆点加成功/失败次数
* ✨ adapter 注册表支持动态模块替换，新增通用 API Key 适配器 GenericProvider，插件站点类型不再按模型名推断端点；协议规格增加运行时覆盖表

#### 插件账号池（account_pool）
* ✨ 声明式账号池：设备码/自定义登录、令牌刷新、账号导入（containers + fieldMap、valueMap）、账号摘要、按账号模型同步（`models.perAccount`），令牌经占位符注入、不进入 JS
* ✨ 网关接入：通用凭据解析、401/传输切换、upstreamStreamOnly 与 SSE 聚合、quota_weighted 选择、额度耗尽冷却、刷新租约
* ✨ 后台与前端：定时刷新与健康检查覆盖插件账号、额度汇总；统一 `/accounts/*` API；账号抽屉支持行状态、区域、设备码登录与图标
* ✨ 抽出凭据解析与 Grok 账号池，统一账号接口和抽屉；Grok 账号同步官方套餐剩余额度并输出标准 entries（额度探测 entries 增加可选的 kind 与 name）

#### 网关与协议
* ✨ 支持跨协议转换模型原生 web_search（请求/响应/流式/历史折叠/用量统计）
* ✨ Claude Code 客户端伪装：对齐真实 CLI 指纹（请求头、billing 块、账号级内存身份、Node TLS 指纹）

#### 界面重构
* ✨ 前端视觉重构为单一深色主题（Apple HIG + Liquid Glass）：全局半透明材质与星空背景、近黑色调、统一的 Dialog / Draw / Sheet 覆盖层、统一的 Card 与表格卡片布局、天琴座登录注册页与流量驾驶舱
* ✨ 各页面按新设计调整：仪表盘、站点、路由、模型、模型价格、请求、审计、用量分析、API 密钥、OAuth、开发者、全局/分组/限流/代理/配置备份/个人信息等设置页
* ✨ 移动端：沉浸式头部与侧边抽屉、新的模型与推理强度选择器、统一的 MobileFilterBar 筛选区与卡片列表
* ✨ 模型体验：输入框浮起质感，右上角新增会动的小螃蟹（Clawd）

#### 账号与安全
* ✨ 会话取真实客户端 IP（优先 CF-Connecting-IP），个人设置会话表格新增设备列（浏览器与系统）
* ✨ OAuth 接入前增加风险免责声明弹窗，需勾选确认后才能继续

### Bug Fixes

* 🐛 Responses 下游对接 Anthropic 上游：thinking 签名经 encrypted_content 往返，缺签名的 thinking 块与工具调用轮降级处理；工具 `input_schema` 顶层 oneOf/anyOf/allOf 合并规整
* 🐛 Responses 转 chat 时合并并行工具调用为同一条 assistant 消息
* 🐛 DeepSeek v4 的 max_tokens 兜底只保留给 Messages 协议并调大到 32768
* 🐛 修正 OpenCode 请求头、默认工具和模型端点映射
* 🐛 Grok 新周期用量为 0 时额度被当作获取失败，改按 0% 已用处理
* 🐛 插件账号池稳定性：刷新换到的新令牌在同一租约内原子落库；刷新失败记账语义修正（取消不掩盖重新授权、落库失败保留上游错误）；账号池刷新改用数据库租约表，修复多实例并发刷新竞争；`site_credentials` 更新改为按列写入避免丢失更新；零账号站点保持正常状态；请求日志不再显示内部 uuid
* 🐛 账号池出站安全：出站请求接入拨号前内网与元数据地址拦截（保留环境代理并放行代理自身地址）；错误文本清洗覆盖单引号 URL 与 userinfo；登录会话加上限并在回收时擦除令牌
* 🐛 插件提供方不可用时，站点刷新与健康检查改为跳过并说明原因，不再自动停用站点或累计失败冷却；调度器、同步 worker 与路由器共用同一个站点服务实例
* 🐛 插件图标与展示：停用后站点图标回退到插件图标接口，提供方不可用时灰化；站点类型显示名与副标题按界面语言展示；`xlyra-plugin` 打包带上声明的站点类型图标
* 🐛 插件列表与受信公钥列表接口在请求被客户端取消时不再记 ERROR 日志，也不再返回 500；调用方取消后仍写入超时健康快照和刷新结果的问题
* 🐛 自动化事件的到期与重试时间只用数据库时钟，消除时钟偏差导致的事件延迟
* 🐛 升级 Go 至 1.26.10，Dockerfile 固定 golang:1.26.10-alpine
* 🐛 前端：开发者页站点查询缓存 key 冲突导致 `sites is not iterable`；悬停浮层同一时间只显示一个；外壳页脚固定在底部；手机端抽屉按钮与返回逻辑、底部胶囊位置、playground 侧边栏与 PWA 顶部留空；登录注册页收起/展开表现；配置备份按钮与开关布局；补回 OAuth 导入的 JSON 拖放、嵌套对话框和刷新图标
* 🐛 升级 zod 至 4.6.7，修复前端生产依赖 npm audit 的高危漏洞

## [1.15.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.15.1...v1.15.2) (2026-10-15)


### Bug Fixes

* 🐛 修复生成的插件类型里泛型被逗号截断的问题 ([c084bba](https://github.com/Yachiyo-5i/xLyra/commit/c084bba532c2d657217a2e6ca61be095f35071d8))

## [1.15.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.15.0...v1.15.1) (2026-10-15)


### Bug Fixes

* 🐛 automation 插件支持站点对象与通用 HTTP 请求，表单控件由 manifest 声明 ([e087657](https://github.com/Yachiyo-5i/xLyra/commit/e087657697a42c16d436144fb65c526dc589d7f0))

## [1.15.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.14.2...v1.15.0) (2026-10-15)


### Features

* 🎸 JS 插件运行时：沙箱、包格式、签名、类型注册表、catalog、automation 运行时与内置插件包 ([0c3c4cc](https://github.com/Yachiyo-5i/xLyra/commit/0c3c4cc4a7f2eee1e47115ac3945c919200b94a4))
* 🎸 xlyra-plugin 命令行工具：init、build、test、run、pack、verify 与各类型脚手架 ([f91ac3a](https://github.com/Yachiyo-5i/xLyra/commit/f91ac3abb3344530f17c75e5ee18ed4e09265e12))
* 🎸 发布插件 SDK 包 @yachiyo-5i/xlyra-plugin-sdk ([978922b](https://github.com/Yachiyo-5i/xLyra/commit/978922badb7af0fc5d0512fc0e17adbd20acb38f))
* 🎸 开发者设置页：插件列表、版本管理、试跑与入口 ([4456a8e](https://github.com/Yachiyo-5i/xLyra/commit/4456a8e348da58220d61280ebe4b1ead523e26b5))
* 🎸 开发者页 automation 配置与权限授予 ([78e498c](https://github.com/Yachiyo-5i/xLyra/commit/78e498ce56f6328839bb82b07308e743ae796fe6))
* 🎸 开发者页中英日文案 ([7a0ddfe](https://github.com/Yachiyo-5i/xLyra/commit/7a0ddfe53a4817ac8243499775226b3bb2b7dd7c))
* 🎸 开发者页数据层与基础组件：插件接口、本地化文本、权限、多选与弹窗视口适配 ([a53b902](https://github.com/Yachiyo-5i/xLyra/commit/a53b902774cb4ed1b718145b91ac582b9540cd2c))
* 🎸 开发者页移动端卡片与版本操作菜单 ([642a3c7](https://github.com/Yachiyo-5i/xLyra/commit/642a3c743a14435205fd1c4de6d57b1ebcc366c0))
* 🎸 开发者页站点多选绑定 ([c1efa97](https://github.com/Yachiyo-5i/xLyra/commit/c1efa9738ab7b8266241f037b0b8c429a4bedb34))
* 🎸 插件管理 API、受信公钥 API 与 automation 事件接入（含额度同步事件） ([6f4b141](https://github.com/Yachiyo-5i/xLyra/commit/6f4b1416458af17b6bc51213a2071af49dab5047))
* 🎸 新增 JS 插件存储与挂载模型 ([5868987](https://github.com/Yachiyo-5i/xLyra/commit/58689875a6b113361558888d54d6a08d56208b98))
* 🎸 新增插件自动化存储 ([a7b633f](https://github.com/Yachiyo-5i/xLyra/commit/a7b633fbfb0fda52f397948dc8adcfed0139e933))
* 🎸 站点与路由接入 JS 插件：额度探测内置化、站点类型插件绑定、错误分类 ([4709653](https://github.com/Yachiyo-5i/xLyra/commit/4709653107f3ed639bed20922f03d16db8243c26))
* 🎸 网关接入 JS 协议插件：端点注册表、协议适配器、签名与错误改写、SystemOne 内置化 ([180c128](https://github.com/Yachiyo-5i/xLyra/commit/180c128e6fbe83bbe33bdccfacb966938118a08e))


### Bug Fixes

* 🐛 .gitignore 的 dev* 不再忽略开发者设置页，并放行 docs/plugins ([874db6d](https://github.com/Yachiyo-5i/xLyra/commit/874db6d1ca642af0d036291784c7ceee5963b267))
* 🐛 Codex 刷新连接时合并而非覆盖已有 metadata，保留 quota 等同步字段 ([8126df8](https://github.com/Yachiyo-5i/xLyra/commit/8126df88c1058175e9667c9522018ec14a97b5fd))
* 🐛 server-run 不再依赖被忽略的 dev-server.sh ([e514dbe](https://github.com/Yachiyo-5i/xLyra/commit/e514dbe1040e5a7d6beec4e3333723c78beb39d5))
* 🐛 全新数据库首次启动时建表改用简单协议 ([7eba484](https://github.com/Yachiyo-5i/xLyra/commit/7eba484f8803071609aa97193cb8d5d5492738e8))
* 🐛 升级 Go 至 1.26.9 及 x/net、x/text，修复 govulncheck 报告的漏洞 ([6663578](https://github.com/Yachiyo-5i/xLyra/commit/666357866fec17ed71995456ebc6ab531871380b))

## [1.14.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.14.1...v1.14.2) (2026-10-05)


### Bug Fixes

* 🐛 按模型应用国模请求体与参数处理 ([a8a6fce](https://github.com/Yachiyo-5i/xLyra/commit/a8a6fce2f9e20f2a529762536f5b5a1391a1e341))

## [1.14.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.14.0...v1.14.1) (2026-09-30)


### Bug Fixes

* 🐛 修复 TypeSafe 模型协议与品牌图标无法识别的问题 ([35c6d53](https://github.com/Yachiyo-5i/xLyra/commit/35c6d539d392946ed09bd49f41a4720ed9eeb2a9))

## [1.14.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.13.2...v1.14.0) (2026-09-30)


### Features

* 🎸 接入 TypeSafe Jev 决策模型并完善品牌展示 ([f00769e](https://github.com/Yachiyo-5i/xLyra/commit/f00769eaefe2526446f44dd2c18c5667eace3711))


### Bug Fixes

* 🐛 让模型缓存价格严格跟随上游目录同步 ([34dd364](https://github.com/Yachiyo-5i/xLyra/commit/34dd36498748773672f113eb948099baefd5f710))

## [1.13.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.13.1...v1.13.2) (2026-09-29)


### Bug Fixes

* 🐛 Playground 按模型站点选择 Gemini 协议 ([52b466f](https://github.com/Yachiyo-5i/xLyra/commit/52b466fb3513f3600cac737b912d5470f091bf73))
* 🐛 修复 Antigravity 额度持久化与面板展示 ([e04bdef](https://github.com/Yachiyo-5i/xLyra/commit/e04bdef1da6c38d486e29220b04088b6527f618b))
* 🐛 修复 Gemini 历史系统消息发送失败 ([3c3e597](https://github.com/Yachiyo-5i/xLyra/commit/3c3e59783300fb46da252597fe8fa20e91a8d770))
* 🐛 修正 Gemini/Antigravity thinkingBudget 字段名 ([870fa9a](https://github.com/Yachiyo-5i/xLyra/commit/870fa9aabbc603ed9f700a0a191f9882ec1ba94f))
* 🐛 将 Antigravity -tiered 归入标准 canonical ([654b98a](https://github.com/Yachiyo-5i/xLyra/commit/654b98a45193463039781a7755b8c930102ad273))

## [1.13.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.13.0...v1.13.1) (2026-09-26)


### Bug Fixes

* 🐛 修复转 Responses 上游时多轮思考回放因缺少 summary 被拒的问题 ([fc72496](https://github.com/Yachiyo-5i/xLyra/commit/fc72496d6388be2a3ee52d8a8bb5008f0fa56858))

## [1.13.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.12.0...v1.13.0) (2026-09-25)

### Features

* 🎸 为 Codex OAuth 增加额度预估 ([d42f1ca](https://github.com/Yachiyo-5i/xLyra/commit/d42f1caec9624fd611fd2b0e0c04791d08308438))

### Bug Fixes

* 🐛 修复下游 completions 转 Gemini 时因用户标识等字段被拒的问题 ([02f3fb5](https://github.com/Yachiyo-5i/xLyra/commit/02f3fb57efc3e82c49e223e3a3ac9f253c57c557))
* 🐛 修复缓存观测数据的备份与升级问题 ([29196fc](https://github.com/Yachiyo-5i/xLyra/commit/29196fc2b8eca960bf583cac04f46f35a269eec4), [026d666](https://github.com/Yachiyo-5i/xLyra/commit/026d666c36fd5d9455c3a51ae5cadbaabb1fce58))
* 🐛 恢复总览页加载时的骨架屏 ([ec422ea](https://github.com/Yachiyo-5i/xLyra/commit/ec422eafa5a70892611cde732105957ec7bb4cdb))
* 🐛 修复超过 32 MiB 的网关请求被拒绝的问题，支持最大 256 MiB 请求体 ([c436f5e](https://github.com/Yachiyo-5i/xLyra/commit/c436f5eefac2790846f3d81a113e884706173c2c))

## [1.12.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.11.0...v1.12.0) (2026-09-23)


### Features

* 🎸 增强模型协议兼容与模型发现能力 ([ae510b5](https://github.com/Yachiyo-5i/xLyra/commit/ae510b58346b37e564726247baf2fefcccbd9b87))


### Bug Fixes

* 🐛 保留 Codex 回调提交的代理 ([551cbe1](https://github.com/Yachiyo-5i/xLyra/commit/551cbe1b925b6ab32008559f2de25515794377e7))
* 🐛 修复前端版本更新后页面未及时切换的问题 ([759a996](https://github.com/Yachiyo-5i/xLyra/commit/759a996465c1ca63d1af910767d9631e379a8ad1))
* 🐛 修复站点筛选配置与下拉显示问题 ([9e3fba9](https://github.com/Yachiyo-5i/xLyra/commit/9e3fba933718865df5e52dd49dd7d03e0d8255bc))
* 🐛 修正冷却 Key 的站点名称显示 ([97c9f9a](https://github.com/Yachiyo-5i/xLyra/commit/97c9f9a7b2c5af1709f3cd5266cbacbd8fe6f745))
* 🐛 修正模型测试的协议和密钥筛选 ([b2f33b1](https://github.com/Yachiyo-5i/xLyra/commit/b2f33b13d2c4c3bc71f07ca13c86aabf5126732f))
* 🐛 加速管理员初始化并改善页面加载缓存 ([7143f14](https://github.com/Yachiyo-5i/xLyra/commit/7143f14c8ad5f081e0104501be22f0901782d89a))
* 🐛 修复 Codex OAuth 令牌交换未遵循所选代理的问题 ([89d254a](https://github.com/Yachiyo-5i/xLyra/commit/89d254a072e11fdf089410f5a351aea8954f7439))
* 🐛 在 Codex 令牌交换前应用提交的代理配置 ([0c58d68](https://github.com/Yachiyo-5i/xLyra/commit/0c58d6842ae0bad4db7eb9e006d3d8a14c2585e5))
* 🐛 修复 Codex OAuth 令牌交换未经过站点代理的问题 ([6597abe](https://github.com/Yachiyo-5i/xLyra/commit/6597abea58d8181354cb38f9fdb59ef7ea756492))

## [1.11.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.10.0...v1.11.0) (2026-09-22)


### Features

* track upstream response models in request logs ([30dba61](https://github.com/Yachiyo-5i/xLyra/commit/30dba61bbacb32045d7626abf53a31c90b2a306a))


### Bug Fixes

* 🐛 修复站点同步丢失模型协议能力的问题 ([428e258](https://github.com/Yachiyo-5i/xLyra/commit/428e2581e20b5df1c47cbe52b39c1fc5be40f1a7))

## [1.10.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.9.1...v1.10.0) (2026-09-20)


### Features

* ✨ 为窗口 Tokens 增加多维明细，并展开节点卡片查看在途数据 ([12f92f1](https://github.com/Yachiyo-5i/xLyra/commit/12f92f11e28a7313d943b24bcea1adad807f17b2))
* ✨ 将流量驾驶舱改为 2.5D 拓扑并按厂商着色 ([d61bab3](https://github.com/Yachiyo-5i/xLyra/commit/d61bab3903bb3aa08eb741171dddd3e27fbafeaf))
* ✨ 按单条请求高亮拓扑，并以穿越网关的粒子表现请求往返 ([9cf0256](https://github.com/Yachiyo-5i/xLyra/commit/9cf0256b4110d582dc1f7e6e57c561a7a890eaa0))
* ✨ 统一驾驶舱圆角与间距，并精简窗口 Tokens 弹窗层次 ([5232aee](https://github.com/Yachiyo-5i/xLyra/commit/5232aeed501a417c593a2d3ac5c9b9fe5eeb3193))
* ✨ 让拓扑节点 hover 时浮起靠近网关，并避免座位跳动 ([e12139a](https://github.com/Yachiyo-5i/xLyra/commit/e12139a1a5f0ca19448dc77117208f4c9b9c15ba))


### Bug Fixes

* 🐛 修复流量驾驶舱合入后无法通过代码检查的问题 ([31ca646](https://github.com/Yachiyo-5i/xLyra/commit/31ca64666bbb56a3ec07d475f81a9361e0a38191))
* 🐛 修复路由页通道列表漏行，并按开启状态与优先级排序 ([c2a83df](https://github.com/Yachiyo-5i/xLyra/commit/c2a83dff480f2183fd1be17edd4247e4438edc11))
* 🐛 加快前端发版检测并避免旧 Service Worker 挡住更新 ([0af9b69](https://github.com/Yachiyo-5i/xLyra/commit/0af9b69371b8c34d12dba8ac24865a83c45314c3))

## [1.9.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.9.0...v1.9.1) (2026-09-20)


### Bug Fixes

* 🐛 修复 API 密钥排序保存失败并简化排序操作 ([a372dd2](https://github.com/Yachiyo-5i/xLyra/commit/a372dd2fb219037360023a766d7bbd28d4a4e886))
* 🐛 修复 DeepSeek 对话请求的历史消息标识兼容问题 ([beab4ca](https://github.com/Yachiyo-5i/xLyra/commit/beab4ca19325cc121855d2caad291efb61069771))
* 🐛 修正 GLM 国内站 Responses 请求端点 ([3459a1e](https://github.com/Yachiyo-5i/xLyra/commit/3459a1e6ce81d41dbd65f941e74f3a9205e18e07))
* 🐛 更新 Codex 图像生成默认宿主模型 ([2f1fb8e](https://github.com/Yachiyo-5i/xLyra/commit/2f1fb8ec542a48372fe0ba09dae3bbf2c3a209fe))
* 🐛 统一文本协议转换与模型可用协议展示 ([34a27ff](https://github.com/Yachiyo-5i/xLyra/commit/34a27ff4010d34297688884b7ce12464fd50a2b0))

## [1.9.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.8.1...v1.9.0) (2026-09-19)


### Features

* 🎸 新增 DeepSeek 账户余额探测 ([8018ad6](https://github.com/Yachiyo-5i/xLyra/commit/8018ad6247244a7577fda241ee0d42dc3b143386), [2ff22ed](https://github.com/Yachiyo-5i/xLyra/commit/2ff22ed93a5e160959c066312f28cdcfb9c5c22a))
* 🎸 新增 Moonshot 账户余额探测 ([33abd21](https://github.com/Yachiyo-5i/xLyra/commit/33abd210b4c4878124b7132ed0feda1aa876ab3c), [25e8a26](https://github.com/Yachiyo-5i/xLyra/commit/25e8a26146cd94f8cf3b3b824695ff18f2a47afb))
* 🎸 智谱模型列表改为动态拉取上游 /models ([140222a](https://github.com/Yachiyo-5i/xLyra/commit/140222a1972862bd586a672446899d80c6cf03b6), [25346c9](https://github.com/Yachiyo-5i/xLyra/commit/25346c92d66eaeaf8dc537cb7b77863004151c6b))


### Bug Fixes

* 🐛 修复 APIKey 权限变更后可用模型未及时更新的问题 ([dc976f6](https://github.com/Yachiyo-5i/xLyra/commit/dc976f643d9b9e47f5c2c7fb6479035eb71eff6f))
* 🐛 修复 Claude Code 和 Codex 自动同步版本后后续请求未使用新版本的问题 ([638224b](https://github.com/Yachiyo-5i/xLyra/commit/638224b69be8005539b84d0f2d552ae179e9a07b))
* 🐛 修复 Codex 账号图像模型协议缺失及权限校验异常导致无法生图的问题 ([2d6e3c8](https://github.com/Yachiyo-5i/xLyra/commit/2d6e3c89d09d527b8ef5341210f40648bd1b0231))
* 🐛 修复 DeepSeek 零余额和负余额更新异常并完善多币种余额展示 ([99d2298](https://github.com/Yachiyo-5i/xLyra/commit/99d2298c2166dff5370b183d2a5e20424974e973))
* 🐛 修复 GLM Coding Plan 积分制套餐额度探测失败 ([9730619](https://github.com/Yachiyo-5i/xLyra/commit/973061972c1c638af01702c581144247d9b03cb8), [7398c5c](https://github.com/Yachiyo-5i/xLyra/commit/7398c5c66382957651ffac408d35a4fa34d5c5cd))
* 🐛 修复 Kimi 兼容适配误改聊天输出上限和自定义站点地址的问题 ([5de8add](https://github.com/Yachiyo-5i/xLyra/commit/5de8add92e8cf172ee518090458cc8bd414e1b21))
* 🐛 修复 Kimi 开放平台 Anthropic Messages 端点缺少 /anthropic 前缀 ([56e63c9](https://github.com/Yachiyo-5i/xLyra/commit/56e63c93760ef6ae21baca8d7f3de0e901f55b76), [8f3c5b7](https://github.com/Yachiyo-5i/xLyra/commit/8f3c5b7b82b1b98a9aad33f6e14dfdd26889e5a9))
* 🐛 修复 Moonshot 零余额更新和欠费余额展示的问题 ([1fe6d26](https://github.com/Yachiyo-5i/xLyra/commit/1fe6d267a4595fb52784d547af3ec7d2a7e28824))
* 🐛 修复同名站点创建失败并统一错误与额度详情提示 ([b3d75aa](https://github.com/Yachiyo-5i/xLyra/commit/b3d75aac0a8477afd120ebc3b9e8d111d792c411))
* 🐛 修复密钥配置返回流程并支持 Gemini 组倍率 ([8fb0def](https://github.com/Yachiyo-5i/xLyra/commit/8fb0def0ed02292ab7c4b6d869ffe8a27012f5b6))
* 🐛 修复思考预算与输出上限冲突导致请求失败的问题 ([f66f357](https://github.com/Yachiyo-5i/xLyra/commit/f66f3575eb17b669044e835347f1f989caeded92))
* 🐛 修复智谱工具额度耗尽误停聊天并补全额度明细展示 ([0bbe10e](https://github.com/Yachiyo-5i/xLyra/commit/0bbe10eef113dacbb8ec050b022bd87ad8c2fa1c))
* 🐛 修复智谱模型列表与上游返回结果不一致的问题 ([eb3f8a2](https://github.com/Yachiyo-5i/xLyra/commit/eb3f8a2cd0efe3108c9d5174deab7c6ae745baff))
* 🐛 修复模型测试使用禁用协议并统一错误详情样式 ([d8057e6](https://github.com/Yachiyo-5i/xLyra/commit/d8057e61fd7a62cdd0636381ab5259b75bfe1957))
* 🐛 修复站点和 OAuth 页面恢复焦点后未自动更新数据的问题 ([7f4929a](https://github.com/Yachiyo-5i/xLyra/commit/7f4929a82b638891d6234fa908fc8040a1abf673))
* 🐛 支持 APIKey 手动排序并同步各设备和相关页面的显示顺序 ([b5dd58a](https://github.com/Yachiyo-5i/xLyra/commit/b5dd58ae6f761cc3a3be0fa1243c890d42fc0852))
* 🐛 统一各页面表格的列宽调整与表头吸顶表现 ([5f9f20e](https://github.com/Yachiyo-5i/xLyra/commit/5f9f20e426bc5465192eaab9b88be9a2b20394e7))
* 🐛 解决余额展示改动的合并冲突 ([acbaed5](https://github.com/Yachiyo-5i/xLyra/commit/acbaed52da06ca1b07ff11910df5f5993d7d8863))

## [1.8.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.8.0...v1.8.1) (2026-09-16)


### Bug Fixes

* 🐛 修复 Codex 图片模型下游请求无可用路由的问题 ([f418d1c](https://github.com/Yachiyo-5i/xLyra/commit/f418d1c34800f6a079e5bf4b00b4d61f187be540))
* 🐛 修复 DeepSeek 工具参数正则导致请求失败 ([bdb2d30](https://github.com/Yachiyo-5i/xLyra/commit/bdb2d30c43c5d89fa304416793d8f2d287b02e41))
* 🐛 修复 DeepSeek 工具参数正则导致请求失败的问题 ([581bcd0](https://github.com/Yachiyo-5i/xLyra/commit/581bcd08b6669b4b73d93d906bf36c723acc4038))
* 🐛 修复智谱和 DeepSeek Responses 端点地址 ([d746aa8](https://github.com/Yachiyo-5i/xLyra/commit/d746aa873d67b4c3fb552103c6a56d3f70918b50))

## [1.8.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.7.2...v1.8.0) (2026-09-14)


### Features

* 🎸 切换模型目录为自维护数据同步 ([9898c22](https://github.com/Yachiyo-5i/xLyra/commit/9898c22aa865609b4ca625f9d731dc1819cd45bc))
* 🎸 完成模型目录协议价格与路由配置接入 ([b1b183f](https://github.com/Yachiyo-5i/xLyra/commit/b1b183f3d0b99a449db063d10a29b80db9d120cb))
* 🎸 完成自维护模型目录同步闭环 ([bc6a813](https://github.com/Yachiyo-5i/xLyra/commit/bc6a813547a8aa801bc249cc45a8e0ba3e1b6f0d))
* 🎸 对齐模型目录价格与站点协议路由 ([cc345a4](https://github.com/Yachiyo-5i/xLyra/commit/cc345a4df3a59142adc4c10b32aef99537b474f3))
* 🎸 接入目录价格与协议能力计费 ([3a1f6e0](https://github.com/Yachiyo-5i/xLyra/commit/3a1f6e0781f446af0364e618a924a56e22ffd4a2))
* 🎸 支持按 API Key 配置模型协议能力 ([fa1fd81](https://github.com/Yachiyo-5i/xLyra/commit/fa1fd81e7aa4c06691731e2da629ad8fc6609dc4))
* 🎸 支持按API Key配置模型协议能力 ([76439fe](https://github.com/Yachiyo-5i/xLyra/commit/76439fe14eb60c8d1b56341e7597360a635da6d1))
* 🎸 移除外部模型信息来源 ([c14962d](https://github.com/Yachiyo-5i/xLyra/commit/c14962d2215965a0779a1015aaa7f2e0e5232ce5))


### Bug Fixes

* 🐛 优化上游链路复用与故障切换等待 ([cbe911f](https://github.com/Yachiyo-5i/xLyra/commit/cbe911fbdf63af6a828f34aa9dbbaf4c6d9debf1))
* 🐛 修复上游余额耗尽后的凭据冷却与恢复 ([7e823f6](https://github.com/Yachiyo-5i/xLyra/commit/7e823f6ec1301589607d5429c556cd528e4885c5))
* 🐛 修复标准模型键不一致导致价格未同步 ([a7369f8](https://github.com/Yachiyo-5i/xLyra/commit/a7369f8af937c2e7a91cef8e0aedc4d0df007e6f))
* 🐛 修正模型目录远程地址 ([bbe3a0c](https://github.com/Yachiyo-5i/xLyra/commit/bbe3a0cdc1d69a1063e4ba21f0aa850549f97abc))

## [1.7.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.7.1...v1.7.2) (2026-09-11)


### Bug Fixes

* 🐛 修复 DeepSeek thinking 与工具调用在同一消息中被拆分导致 400 错误 ([543fd82](https://github.com/Yachiyo-5i/xLyra/commit/543fd82994cb3119d342277b1e2c79759928af59))
* 修复 DeepSeek thinking 与工具调用被拆分到两条消息导致 400 错误 ([1467211](https://github.com/Yachiyo-5i/xLyra/commit/1467211c8f26d050f84889ec360018978e711c71))

## [1.7.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.7.0...v1.7.1) (2026-09-11)


### Bug Fixes

* 🐛 修复 DeepSeek 思考内容在跨协议请求中的回传 ([3a0667e](https://github.com/Yachiyo-5i/xLyra/commit/3a0667ee4b2be7e70ad0e2c4bc6936a49fe18ffa))
* 🐛 修复 DeepSeek 思考内容跨协议回传 ([ea48907](https://github.com/Yachiyo-5i/xLyra/commit/ea48907c4846abc9643e85e4934f4f2ab452ed84))
* 🐛 修复模型体验会话 ID 必须为 UUID ([c099bfe](https://github.com/Yachiyo-5i/xLyra/commit/c099bfe049ceb57e95a485f229b1365c2f866280))
* 🐛 修复模型体验会话 ID 必须为 UUID ([2e82b8a](https://github.com/Yachiyo-5i/xLyra/commit/2e82b8ab549b96152886a8fb390d2a9c88d4951e))

## [1.7.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.6.0...v1.7.0) (2026-09-08)


### Features

* 🎸 GLM Code 站点支持 Coding Plan 额度探测 ([d7e87bd](https://github.com/Yachiyo-5i/xLyra/commit/d7e87bdb51c9dd73e023387320d2a75d1dfaffe6))
* 🎸 GLM Code 站点支持 Coding Plan 额度探测 ([f452cbc](https://github.com/Yachiyo-5i/xLyra/commit/f452cbc45cc1e9cf298fe5ba0b46817ee7d66d7a))
* 🎸 Kimi 与 GLM 订阅额度耗尽时自动暂停调用直至额度重置 ([260ddd4](https://github.com/Yachiyo-5i/xLyra/commit/260ddd4de89ee46c15fd95cbbed462cdb4a018cc))
* 🎸 Kimi 与 GLM 订阅额度耗尽时自动暂停调用直至额度重置 ([af9ee82](https://github.com/Yachiyo-5i/xLyra/commit/af9ee8233458550f3736d8f92300c0c6db1706a5))
* **server,web:** ✨ 推理强度档位规范、映射与前端选择器联动 ([55bf307](https://github.com/Yachiyo-5i/xLyra/commit/55bf3071b8d0cc70d6f098dcbda86769a83c9323))
* **server,web:** ✨ 推理强度档位规范、映射与前端选择器联动 ([dd7f524](https://github.com/Yachiyo-5i/xLyra/commit/dd7f52469534d3071cb406bf3db126ae1df81344))


### Bug Fixes

* 🐛 修复额度探测在中转站点误开启及订阅窗口信息异常的问题 ([7775b0d](https://github.com/Yachiyo-5i/xLyra/commit/7775b0db4cc8c8e477b5ca738906e171a94ed63f))

## [1.6.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.5.2...v1.6.0) (2026-09-06)


### Features

* 🎸 支持为 API 密钥设置计费倍率 ([cc82051](https://github.com/Yachiyo-5i/xLyra/commit/cc820512c18bc1b67cb206b5f17c785a0f1b3884))
* 🎸 统一数据库迁移执行与版本追踪 ([3f69d42](https://github.com/Yachiyo-5i/xLyra/commit/3f69d42a4b4a4dcf4132c1beeb217dd8660c5e7c))
* 支持 API 密钥计费倍率 ([86beca8](https://github.com/Yachiyo-5i/xLyra/commit/86beca87a4e6e6473d43a2a6523d39462c4b2521))


### Bug Fixes

* 🐛 将 OAuth 刷新并发修复合入主分支 ([b528957](https://github.com/Yachiyo-5i/xLyra/commit/b528957f07790893f6938d190bdcbe47146385e3))
* 🐛 避免并发 OAuth 刷新失败后重复请求上游 ([854adf7](https://github.com/Yachiyo-5i/xLyra/commit/854adf79160dcbade47fd8395ab444d1fff8b599))

## [1.5.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.5.1...v1.5.2) (2026-09-05)


### Bug Fixes

* 🐛 补上 codex OAuth 刷新路径的 gpt-image-2 路由模型 ([20cc6be](https://github.com/Yachiyo-5i/xLyra/commit/20cc6bec92a7fb402eac48f46a5d0a5777d0424d))
* 🐛 补上 codex OAuth 刷新路径的 gpt-image-2 路由模型 ([be130d9](https://github.com/Yachiyo-5i/xLyra/commit/be130d9830be9645ed09af5cf8653989ecf4a18a))

## [1.5.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.5.0...v1.5.1) (2026-09-05)


### Bug Fixes

* 🐛 gpt-image-2 路由条目补 id 字段，避免被站点层解析丢弃 ([823db4c](https://github.com/Yachiyo-5i/xLyra/commit/823db4cbdf12336c667edf9472543e779ad155f2))
* gpt-image-2 路由条目补 id 字段，避免被站点层解析丢弃 ([a294099](https://github.com/Yachiyo-5i/xLyra/commit/a294099f377b4830a3545d43c4fd29f56108ba4c))

## [1.5.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.4.0...v1.5.0) (2026-09-04)


### Features

* 🎸 Codex 客户端版本动态化，模型列表不再用静态兜底 ([85a4894](https://github.com/Yachiyo-5i/xLyra/commit/85a4894bde184d0c21b7139b7c08ace48a3c3bed))
* Codex 客户端版本动态化，模型列表不再用静态兜底 ([c715ea0](https://github.com/Yachiyo-5i/xLyra/commit/c715ea051df181b3542aa2601648d7213d550c09))


### Bug Fixes

* 🐛 Codex 模型价格改为跟随价格目录自动更新，官方调价可自动生效 ([bb73757](https://github.com/Yachiyo-5i/xLyra/commit/bb7375765b5dfe2f2654c1a506197509d63c5cb3))
* 🐛 Codex 模型价格改为跟随价格目录自动更新，官方调价可自动生效 ([729e4db](https://github.com/Yachiyo-5i/xLyra/commit/729e4dbcee6f8e844547552e19a0518ac36c492a))

## [1.4.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.3.1...v1.4.0) (2026-08-27)


### Features

* 🎸 API 密钥轮换与分钟级到期 ([e61b215](https://github.com/Yachiyo-5i/xLyra/commit/e61b21526ae78e8c8f44779cb14c10629e551bef))
* 🎸 支持轮换 API 密钥，一键更换密钥并保留站点模型用量等全部配置 ([078bdc5](https://github.com/Yachiyo-5i/xLyra/commit/078bdc5c4dce849ed4a403f5a00224af7724fd72))


### Bug Fixes

* 🐛 密钥到期时间支持精确到分钟，到期后自动置为停用状态 ([02f3244](https://github.com/Yachiyo-5i/xLyra/commit/02f324498592502ca0b843a747cd1673e79a372f))

## [1.3.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.3.0...v1.3.1) (2026-08-27)


### Bug Fixes

* 🐛 优化 Playground 操作布局并完善代码复制与生图结果信息展示 ([1d37ac0](https://github.com/Yachiyo-5i/xLyra/commit/1d37ac03d37fd058bd691c764697f2f1ad68797a))
* 🐛 修复弹窗面板内下拉列表无法滚动导致选项显示不全的问题 ([8b71f29](https://github.com/Yachiyo-5i/xLyra/commit/8b71f291c60b97c93d8cf8aae9e364145836b942))

## [1.3.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.2.3...v1.3.0) (2026-08-24)


### Features

* 🎸 新增模型体验记录跨浏览器持久化与断点续传 ([74815a5](https://github.com/Yachiyo-5i/xLyra/commit/74815a54a1ed1561c4ff5f8d05dd506eebe59ad7))


### Bug Fixes

* 🐛 修复备份恢复未覆盖模型体验会话记录与图片附件的问题 ([8225a03](https://github.com/Yachiyo-5i/xLyra/commit/8225a036773d567844e16d94a8d5ffe8ae2a4cbf))
* 🐛 修复大文件备份还原限制并完善后台恢复体验 ([48fb46d](https://github.com/Yachiyo-5i/xLyra/commit/48fb46d783cbedc19ff94973ea89d2dee2d4abbd))
* 🐛 修复站点与 API 密钥提交阻塞并完善后台同步体验 ([7516eb4](https://github.com/Yachiyo-5i/xLyra/commit/7516eb4ab429314d32de423e8c32671b9c51e38d))
* 🐛 修正订阅限额冷却校准的查询写法以符合项目数据库访问规范 ([a7e48d4](https://github.com/Yachiyo-5i/xLyra/commit/a7e48d4906918fe018d93db744c768dc80481f95))
* 🐛 修复 TokenPlan 周限额冷却时间计算错误 ([5d963e1](https://github.com/Yachiyo-5i/xLyra/commit/5d963e1151884e61acdc381ce6bb8d9e24e73622))

## [1.2.3](https://github.com/Yachiyo-5i/xLyra/compare/v1.2.2...v1.2.3) (2026-08-23)


### Bug Fixes

* 🐛 修复推理强度错误提示并兼容完整枚举 ([fe411f2](https://github.com/Yachiyo-5i/xLyra/commit/fe411f271e54b84f799de4b9b732796246d17f77))
* 🐛 修复跨协议请求的缓存用量回传与上游协议选择 ([5bf3af7](https://github.com/Yachiyo-5i/xLyra/commit/5bf3af7ade801294be920f1862c35164d52335f3))
* 🐛 减少页面认证初始化等待并防止登录状态被错误缓存 ([5cba9ea](https://github.com/Yachiyo-5i/xLyra/commit/5cba9ea27f3cfb34f39aee9f070ddd263d14c361))

## [1.2.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.2.1...v1.2.2) (2026-08-18)


### Bug Fixes

* 🐛 修复 Grok 请求在不同协议间的参数兼容问题 ([3d91bb2](https://github.com/Yachiyo-5i/xLyra/commit/3d91bb293d2d0c83dd7c013111add8cba388afff))
* 🐛 修复中等屏幕下仪表盘指标卡布局不一致和内容换行的问题 ([f882361](https://github.com/Yachiyo-5i/xLyra/commit/f882361b8be73d9f29d240c724a62ab653ff1f88))
* 🐛 修复费用构成浮层层级及移动端无法关闭的问题 ([fabf448](https://github.com/Yachiyo-5i/xLyra/commit/fabf4481878f713b8636f6029cff13e1bb6c724d))
* 🐛 完善上游站点套餐额度详情与移动端查看体验 ([f5b622f](https://github.com/Yachiyo-5i/xLyra/commit/f5b622f810fb76a0357decc449601c1418be9ddf))
* 🐛 用量分析新增昨天时间范围与单日按小时查看，筛选切换即时生效 ([aa70a7b](https://github.com/Yachiyo-5i/xLyra/commit/aa70a7b7381b425986163fb1bce9ab1b7f5c4c8e))
* 🐛 用量分析新增昨天时间范围与单日按小时查看，筛选切换即时生效 ([a2684b5](https://github.com/Yachiyo-5i/xLyra/commit/a2684b536d441dd72c541f3bda6196d20a4c345c))
* 修复 Grok Responses 跨协议参数兼容问题 ([d55a605](https://github.com/Yachiyo-5i/xLyra/commit/d55a605fa66377125074b56c9acd4405a5184b49))

## [1.2.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.2.0...v1.2.1) (2026-08-16)


### Bug Fixes

* 🐛 修复推理强度选择错误及失败请求无法显示模型的问题 ([e8f097b](https://github.com/Yachiyo-5i/xLyra/commit/e8f097b937f6773c26459c0b838e93c1cd93f212))

## [1.2.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.1.3...v1.2.0) (2026-08-15)


### Features

* 🎸 新增用量分析独立页面与接口 ([481ac74](https://github.com/Yachiyo-5i/xLyra/commit/481ac7445aff0bc010c6a8bb96498abe2617dc45))


### Bug Fixes

* 🐛 优化前端页面交互与展示体验 ([c460b06](https://github.com/Yachiyo-5i/xLyra/commit/c460b06520da7de56c9a66209e788467b0b7fc1e))
* 🐛 修复 Codex 上游拒绝新版提示缓存参数的问题 ([f6e99a9](https://github.com/Yachiyo-5i/xLyra/commit/f6e99a912e3bacdaaac456bbee416be606ab0822))
* 🐛 修复 Codex 上游拒绝新版提示缓存参数的问题 ([ac4be61](https://github.com/Yachiyo-5i/xLyra/commit/ac4be61c3805232605e96a96fafcbce852a6babb))
* 🐛 修复服务模式和推理强度识别及长上下文计费展示 ([74e0109](https://github.com/Yachiyo-5i/xLyra/commit/74e01096a1cb8ac312a239a0002262ed6d9eb0f9))
* 🐛 修复缓存用量统计与缓存写入汇总缺失 ([c2dfa88](https://github.com/Yachiyo-5i/xLyra/commit/c2dfa88298980d2ca72b34783cc386eca28a7a83))
* 🐛 修复缓存观测亲和在多凭据轮转时永久失效及相关问题 ([ba40d7f](https://github.com/Yachiyo-5i/xLyra/commit/ba40d7fa433929df57695241a5d7868a0ab0c639))
* 🐛 修复缓存路由观测、凭据缓存域更新和历史缓存费用统计异常 ([a2be711](https://github.com/Yachiyo-5i/xLyra/commit/a2be71184e9825a2756242eefdc0fba9b76dd49d))
* 🐛 修复请求切换记录不准确并优化切换过程展示 ([ad5e9d7](https://github.com/Yachiyo-5i/xLyra/commit/ad5e9d7b92583f266125428dee37ed4b6b19f3fd))
* 🐛 修正缓存观测的亲和判定与过期边界 ([4ab1815](https://github.com/Yachiyo-5i/xLyra/commit/4ab18156a71126f70c5fca093f96edd7e56a8ce4))
* 🐛 升级 Go 版本至 1.26.6 修复标准库安全漏洞 ([6593a94](https://github.com/Yachiyo-5i/xLyra/commit/6593a94beabb0359024060ea22f65960b9aab829))
* 🐛 合并主分支并解决缓存统计与请求记录冲突 ([485abf3](https://github.com/Yachiyo-5i/xLyra/commit/485abf380f03bc8dc4d197e72fdd2bea74710664))
* 🐛 增加多轮请求的缓存命中观测 ([b6245c3](https://github.com/Yachiyo-5i/xLyra/commit/b6245c3c0213c7e08b1a1d06547f6ff68ff53c86))
* 🐛 精简 Dashboard 页面并减少首屏加载的数据量 ([a58fd3d](https://github.com/Yachiyo-5i/xLyra/commit/a58fd3ded5dfaa880fd7369b69ff3ff5a42c83cd))
* 🐛 补齐缓存写入的结构化成本统计 ([b0fd71f](https://github.com/Yachiyo-5i/xLyra/commit/b0fd71f7591b116ab4532c5f085837531a506a97))
* improve gateway failover diagnostics ([34a6275](https://github.com/Yachiyo-5i/xLyra/commit/34a6275bb24f7bcd76459054fb17f48368947295))


### Performance Improvements

* ⚡️ 加快大数据备份恢复并展示实时进度 ([708eea7](https://github.com/Yachiyo-5i/xLyra/commit/708eea78e0b407e63e018f5b5eb5955efe329737))

## [1.1.3](https://github.com/Yachiyo-5i/xLyra/compare/v1.1.2...v1.1.3) (2026-08-13)


### Bug Fixes

* 🐛 修复多级代理连接 Codex OAuth 时命名空间工具调用被拒绝的问题 ([02d51a0](https://github.com/Yachiyo-5i/xLyra/commit/02d51a041e80f56514dd7c82d151e31839914efb))
* 🐛 修复站点自定义请求头删除后仍被保留的问题 ([07cc9b2](https://github.com/Yachiyo-5i/xLyra/commit/07cc9b25458772f6c6b6821c355ff7959b3373d5))

## [1.1.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.1.1...v1.1.2) (2026-08-12)


### Bug Fixes

* 🐛 优化模型体验区的附件粘贴和生图参数选择体验 ([f58756b](https://github.com/Yachiyo-5i/xLyra/commit/f58756bc5664e4a657f714f8500e73d93d60b6fb))
* 🐛 修复流式响应在业务输出前无法自动切换路由的问题 ([6e15c32](https://github.com/Yachiyo-5i/xLyra/commit/6e15c3200d489a8ec23b7be8ad5ac880ccbacbf2))

## [1.1.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.1.0...v1.1.1) (2026-08-11)


### Bug Fixes

* 🐛 修复流式响应空输出被误判并确保过载后正常切换的问题 ([4cf4019](https://github.com/Yachiyo-5i/xLyra/commit/4cf4019fd760e783b8f4370fe944fa8c96319c43))
* fail over pre-output response overloads ([add5a77](https://github.com/Yachiyo-5i/xLyra/commit/add5a77e11b4392310413218c68f7017895734e5))
* fail over pre-output response overloads ([d7af1f2](https://github.com/Yachiyo-5i/xLyra/commit/d7af1f2b6ca3cce73356f75e8b0649f49ac6c062))
* preserve pre-output stream failure semantics ([c1816f4](https://github.com/Yachiyo-5i/xLyra/commit/c1816f445b264d7a49126b113f52293c08854288))

## [1.1.0](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.6...v1.1.0) (2026-08-10)


### Features

* ✨ 增强请求日志列表与详情 ([ab152bb](https://github.com/Yachiyo-5i/xLyra/commit/ab152bbe43d5500a01375e1b298034145161343a))
* ✨ 增强请求日志列表与详情 ([d58ac9c](https://github.com/Yachiyo-5i/xLyra/commit/d58ac9cd755c719ece7fdaed996bf7da8296979b))


### Bug Fixes

* 🐛 优化请求日志的计费展示与移动端布局 ([a18832e](https://github.com/Yachiyo-5i/xLyra/commit/a18832eafe03f6fb01100faaab6c53ef6643fb91))
* 🐛 修复 OpenCode 冷门模型无法路由的问题 ([578f65f](https://github.com/Yachiyo-5i/xLyra/commit/578f65f881aca82633e8f9d7ffae18e8d4d2c91a))
* 🐛 修复超长工具调用标识冲突导致的请求失败 ([35735fa](https://github.com/Yachiyo-5i/xLyra/commit/35735fa02b0a1867416a9ef783ea938291310026))

## [1.0.6](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.5...v1.0.6) (2026-08-09)


### Bug Fixes

* 🐛 优化 Playground 模型选择体验 ([3bfbb73](https://github.com/Yachiyo-5i/xLyra/commit/3bfbb7305cf6e22f1dce6453607f8dc19941a152))
* 🐛 修复上游非成功响应被误判为语义失败导致触发错误冷却的问题 ([b2febf3](https://github.com/Yachiyo-5i/xLyra/commit/b2febf3d0006e494151be2d7e18b2b1eb6118e8b))
* 🐛 修复自动备份未清理历史版本导致存储空间占满的问题 ([b413996](https://github.com/Yachiyo-5i/xLyra/commit/b41399666c0688fa22e54100fd0c93c7c9ff21eb))
* **gateway:** classify semantic upstream failures ([7b4f449](https://github.com/Yachiyo-5i/xLyra/commit/7b4f449181c5a186bb500ffd68f4267724c36f98))
* **gateway:** 正确处理 2xx 响应中的语义上游失败 ([bd59404](https://github.com/Yachiyo-5i/xLyra/commit/bd59404e89474cbb5ede860cbd89b4a3aa3c81a3))

## [1.0.5](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.4...v1.0.5) (2026-08-07)


### Bug Fixes

* 🐛 修复跨协议请求因缺少输出长度导致上游调用失败 ([eec7dd1](https://github.com/Yachiyo-5i/xLyra/commit/eec7dd166b61a44dd6eda50f982641457646f16b))

## [1.0.4](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.3...v1.0.4) (2026-08-07)


### Bug Fixes

* 🐛 支持下游密钥可重置总限额并保留累计消耗 ([b1ece74](https://github.com/Yachiyo-5i/xLyra/commit/b1ece74a663993d1ed446c5944fdbd0c431ef18e))

## [1.0.3](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.2...v1.0.3) (2026-08-06)


### Bug Fixes

* 🐛 修复 MiMo TTS 模型无法通过语音合成接口调用的问题 ([c8d3ed8](https://github.com/Yachiyo-5i/xLyra/commit/c8d3ed8de0d134d5fe0b3569be391e0658d354f2))
* 🐛 修复小米 MiMo V2.5 语音合成模型的调用兼容问题 ([4f7aaf1](https://github.com/Yachiyo-5i/xLyra/commit/4f7aaf1639faabdaa11f3ec3d1dc45de724fb9b2))
* 🐛 避免订阅额度耗尽后持续请求上游 ([ed384ec](https://github.com/Yachiyo-5i/xLyra/commit/ed384ec004ad56b45d57e36d9d38da6b5c7de31e))

## [1.0.2](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.1...v1.0.2) (2026-08-05)


### Bug Fixes

* 🐛 Anthropic 类型站点支持余额探测配置与 Apikey 组倍率，新增 Apikey 时明文显示输入内容 ([0ee215c](https://github.com/Yachiyo-5i/xLyra/commit/0ee215c87a73d6e800057bd023ff305e2bda0f3f))
* 🐛 修复 Anthropic 模型缓存用量统计错误及模型映射后缓存失效的问题，并优化请求列表缓存数据展示 ([b4ae4b0](https://github.com/Yachiyo-5i/xLyra/commit/b4ae4b076b0748c43db71f116e8dc125813c47d3))
* 🐛 修复 Codex 等 OAuth 站点模型价格未随标准价格变更实时同步的问题 ([4064a10](https://github.com/Yachiyo-5i/xLyra/commit/4064a10f26523fe86f7939175defe06b99eba67d))

## [1.0.1](https://github.com/Yachiyo-5i/xLyra/compare/v1.0.0...v1.0.1) (2026-08-05)


### Bug Fixes

* 修复 OAuth 账号站点的模型价格未随标准价格变更自动更新的问题 ([542e90e](https://github.com/Yachiyo-5i/xLyra/commit/542e90e5540ef68d5df4dc2d07421a5f89ab87a0))
* 修复下游 responses 协议转发上游任意协议时响应内容可能丢失空格的问题 ([5542358](https://github.com/Yachiyo-5i/xLyra/commit/55423582501bdebb9c6af6fc84dc22e712a47cb1))

## 1.0.0 (2026-08-04)


### Features

* initial public release ([d6ee5c5](https://github.com/Yachiyo-5i/xLyra/commit/d6ee5c5e1b4049f9283b8f3bf2393c52d291851a))
