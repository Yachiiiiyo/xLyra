# 命令行工具 xlyra-plugin

开发者本地测试和线上运行用的是**同一份运行时代码和同一个 `moejs` 引擎版本**，所以本地通过的样本，上传后也会通过。打包使用内置的 esbuild 将多文件 TS/JS 打成单文件 `plugin.js`，不需要安装 Node。（注：Go 宿主运行时另提供 `jsplugin.NewPluginTS` 支持单文件 TypeScript 直编，但 CLI 与 `.xlp` 归档仍统一构建和校验 `plugin.js`。）

```bash
xlyra-plugin --version
```

打印 CLI 版本、`apiVersion`、`hostApi` 和 `moejs` 引擎版本。

所有带 `[dir]` 的命令，目录默认是当前目录。

## init

```bash
xlyra-plugin init [--template NAME] [--id ID] [--name NAME] <dir>
```

在 `dir` 里创建新工程（目录必须不存在或为空）。`--template`（`--kind` 是同一选项的别名）默认 `quota_probe`，可以是 `xlyra-plugin kinds` 列出的任意一种（包括 `site_provider`），也可以指定 `gateway_pipeline`（或 `gateway-pipeline`）创建网关文本管线起步工程。除 `site_provider` 外，每种模板都带有可直接通过的示例样本。`site_provider` 的模板没有样本，空样本的包也可以 `test` 通过。`--id` 默认由目录名推导，不合规会报错并提示用 `--id`。`--name` 不写则用 id。生成的工程可以直接 `test` 通过。

## kinds

```bash
xlyra-plugin kinds
```

列出当前版本支持的所有 `kind`，包括工作方式（族）、导出的函数、凭据，以及管理员怎么让它生效。选择方法见[选择类型](./kinds.md)。

## build

```bash
xlyra-plugin build [--out dist] [dir]
```

把 `src/index.ts`（或 `src/index.js`）用内置 esbuild 打成单文件 ES2023 模块 `plugin.js`，并自动将实际导出的已知钩子名写入 `exports`、将源码摘要写入 `sha256["plugin.js"]`，写出：

```text
dist/manifest.json     已填好 exports 与 sha256
dist/plugin.js
dist/fixtures/*.json
```

构建后还会像服务端那样执行编译、静态字节码校验（`bytecode.Verify`）与模块加载，所以清单错误、`meta` 不匹配、语法/字节码问题在这里就能发现。清单里的顶层 `icon` 和 `siteProvider.display.icon` 会从工程目录复制进输出；顶层 `icon` 会做[图标检查](./manifest.md#图标)，不合格就失败，错误信息会写明实测值和改法。外部依赖和动态 `import()` 会被拒绝，见[限制](./limits-and-security.md)。`build` 不会删除 `dist/` 里已有的 `.xlp`。

## test

```bash
xlyra-plugin test [--watch] [dir]
```

构建后逐个运行 `fixtures/*.json`，打印每个样本的结果；有失败则退出码为 1。`--watch` 监听 `manifest.json`、`src/`、`fixtures/` 和 `tsconfig.json` 的变化并自动重跑。

## dry-run

```bash
xlyra-plugin dry-run [--params params.json] [--host-options opts.json] [--endpoint PATH] [--model MODEL] [--dir DIR] <file | ->
```

对声明了 `gatewayPipeline` 段的插件做离线管线预览，不发任何网络请求、也不使用片段缓存：

- `--params`：JSON 文件路径，覆盖 `gatewayPipeline.inputs` 中的绑定参数（未指定的非实体参数自动采用清单 `default`）。
- `--host-options`：JSON 文件路径，传入管线宿主选项。
- `--endpoint`：下游端点路径，默认 `/v1/chat/completions`。
- `--model`：传给 `inspectSegments` 的模型名，默认 `dry-run`。
- `--dir`：插件工程目录，默认当前目录 `.`。
- `<file | ->`：必填，输入文件路径；传 `-` 表示从标准输入读取。内容为 JSON 对象时直接作为请求体运行；为纯文本时自动包装成单条 `user` 消息的 `/v1/chat/completions` 请求，并在结果 JSON 中额外输出 `transformed_text`。

标准输出为缩进 JSON，包含 `outcome`、`changed`、`metrics`、`findings`（每条含 `segment_id`、`role`、`field`、`start`、`end`、`type`、`source`、`score`、`placeholder`、`applied`）、`transformed_payload`，以及失败时的 `failure`（`status`、`code`、`message`）。

## run

```bash
xlyra-plugin run --key-env VAR [--base-url URL] [--site-type T] [--credential-type C] [--hook HOOK] [dir]
```

对真实站点发一次分步调用。

- `--key-env`：存放密钥的环境变量名。会向站点带密钥的类型**必填**。密钥只会写进 `Authorization` 头，插件看不到。
- `--base-url`：站点地址，会按清单的 `baseURLMode` 整理；省略则使用 `defaultBaseURL`。`site_provider` 没有 `baseURLMode`，按 `as_is` 整理，省略时用 `officialBaseURL`。
- `--site-type`：传给插件的 `ctx.siteType`，默认 `custom`。
- `--credential-type`：传给插件的 `ctx.credentialType`，默认 `api_key`。
- `--hook`：用于 `site_provider` 或无 `kind` 插件，必填。`site_provider` 取值 `probe`、`listModels` 或 `check`（无 `kind` 插件额外支持 `detect`）。`classify`、`rewritePayload`、`transformStream`、`createStreamFilter`、`signRequest`、`onSchedule`、`inspectSegments` 以及账号池的 `startLogin`、`pollLogin`、`refreshTokens`、`summarizeAccount` 不支持 `run`，但均可以写进 `fixtures/*.json` 用 `xlyra-plugin test` 验证（`gatewayPipeline` 还可配合 `dry-run` 预览）。其它带 `kind` 的插件写了 `--hook` 会报错。
- 实际发出的每个请求会打印到标准错误，插件的日志也会打印，最终结果以 JSON 输出到标准输出。

支持会向站点取数据的类型：`quota_probe`、`model_list`、`credential_check`、`site_detect`，以及 `site_provider` 与无 `kind` 插件的上述分步钩子。`site_detect` 不带凭据，不需要 `--key-env`，请求也不会有 `Authorization` 头。`protocol`、一次性函数、流式钩子、账号池钩子和 `gatewayPipeline` 请用 `test`（或 `dry-run`）验证。本地 `run` 只做同源限制，不套用 `site_provider` 的主机白名单。

## pack

```bash
xlyra-plugin pack [--sign key.pem] [--out file.xlp] [dir]
```

构建并生成 `.xlp`，默认写到 `dist/<id>-<version>.xlp`。写文件前会做和 `verify` 相同的全部检查（包括字节码校验、静态分析输出、样本自检和图标检查），不合格就不生成，并打印完整错误。`--sign` 用私钥签名。

## verify

```bash
xlyra-plugin verify <package.xlp>
```

对已有的 `.xlp` 包做服务端上传时的全部检查：结构、清单、`sha256["plugin.js"]`、签名、编译与静态字节码校验（`bytecode.Verify`）、静态分析（`bytecode.Analyze`）、加载、运行全部样本，以及顶层 `icon` 的图标检查。通过时打印摘要、包 SHA256 与静态分析行，例如：

```text
ok  example.minimal@1.0.0 (site_provider), 4 fixtures, signed by <fingerprint>
sha256 <package-sha256>
analysis functions=6 instructions=142 regexps=2 globals=JSON,Math
icon assets/icon.png 256x256 不透明 100% 亮度 0.62
```

`analysis` 行汇报模块的函数数（`functions`）、指令数（`instructions`）、正则字面量数（`regexps`）与引用的全局变量列表（`globals`，无引用时为 `none`）。若字节码中检测到受限或需注意的语法结构，会在行尾追加 `flags=...`（可能包含 `eval`、`Function`、`import()`、`with`、`async` 的组合；其中 `async` 用于提示异步钩子会返回 `Promise` 并在运行时报 `js_shape_error`）。声明了顶层 `icon` 时会再打印一行实测尺寸、不透明占比与亮度。

## keygen / sign

```bash
xlyra-plugin keygen [--out key.pem]
xlyra-plugin sign <package.xlp> --key key.pem [--out signed.xlp]
```

`keygen` 生成 ed25519 私钥（PEM，权限 `0600`），并输出 `public_key`（base64）和 `fingerprint`。**私钥请自己妥善保管，不要提交到仓库。** `sign` 给已有的包附上签名；`pack --sign` 是同样的事。

## publish

```bash
export XLYRA_CLOUD_TOKEN='xlh_…'
xlyra-plugin publish <package.xlp> --changelog "..." [--changelog-file FILE] [--cloud URL] [--token T] [--no-wait]
```

把已签名的 `.xlp` 提交到 xLyra Cloud。默认 xLyra Cloud 是 `https://xlyra.yachiyo.im`（与当前 `root.json` 的 `endpoints.hub` 相同）。`--changelog` 和 `--changelog-file` 二选一，必填。

发布前会先做和 `verify` 一样的本地检查，并且要求包已经签名。通过后上传到 `POST /api/dev/token/submissions`，打印提交编号，然后每 3 秒查询状态，直到进入待审核、已批准、已发布、已退回、检查未通过或已撤回。默认最多等 10 分钟。`--no-wait` 提交后立刻返回。

`--token` 会进入 shell 历史，请改用环境变量 `XLYRA_CLOUD_TOKEN`。命令行参数优先于环境变量。xLyra Cloud 地址必须是 https；只有 `http://127.0.0.1` 和 `http://localhost` 可以用来连本地测试的 xLyra Cloud。令牌不会出现在命令的输出里。

完整路径是 `keygen` 生成密钥、`pack --sign` 或 `sign` 签名，再到 `publish`。见[发布](./publishing.md)。
