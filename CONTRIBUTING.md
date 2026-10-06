# 贡献指南

感谢你愿意向 AniaBot 插件市场提交插件。本仓库的插件会被安装到所有用户的 Bot 中并以 Bot 的权限运行，因此**质量与安全是第一优先级**。

## 提交流程

1. Fork 本仓库，新建分支 `plugin/<plugin-id>`
2. 在 `plugins/<plugin-id>/` 下创建插件，包含：
   - `plugin.json`（见 [docs/plugin-spec.md](docs/plugin-spec.md)）
   - `README.md`（介绍功能、命令、配置、注意事项）
   - Go 源码（可参考 [examples/example](examples/example) 示例插件，仅作开发参考，不进入市场列表）
3. 本地校验：`bash scripts/validate.sh`（会编译插件；`index.json` 与 README 插件列表由 CI 在合并到 main 后自动同步，无需手动维护）
4. 提交 PR，标题格式：`plugin: 新增 <插件名> (<id>)` 或 `plugin: 更新 <插件名> (<id>)`
5. CI 自动校验通过后，等待维护者审查合并

## 审查清单（维护者人工检查）

- [ ] `plugin.json` 字段合法，`id` 与目录名一致，不与现有插件冲突
- [ ] 源码无 `init()` 副作用（网络/文件/进程）
- [ ] 无窃取凭据/配置的行为（不得读取 `data/`、环境变量中的密钥并外发）
- [ ] 网络请求仅发往合理目标（作者自己的服务需说明用途），默认不启用
- [ ] 文件读写限定在插件自有命名空间或 `data/` 下安全位置
- [ ] 不执行任意 shell 命令（确有需要必须默认关闭、管理员确认）
- [ ] 第三方依赖尽量少、来源可信；`go.mod` 变更需随 PR 说明
- [ ] README 写清功能、命令、权限要求与配置项
- [ ] `index.json` 与 README 插件列表可暂不更新：合并到 main 后 CI 会自动重新生成并提交（本地运行 `validate.sh` 若提示差异也无需处理）

## 插件开发提示

- 本地开发时把插件目录复制到 AniaBot 源码树：`cp -r plugins/<id> <AniaBot>/custom/plugins/<id>`，然后 `cd <AniaBot> && go run ./tools/plugingen && go run cmd/main.go`
- 插件支持 `ConfigSchema()`，声明后面板「配置管理」会自动渲染表单，无需改面板代码
- 平台能力用类型断言探测（如 `bot.QQ`），写 QQ 专属功能时在 `plugin.json` 的 `platforms` 里声明 `["qq"]`
- 插件代码与框架 API 绑定编译期版本：编译不过就装不上，升级 AniaBot 后如 API 变化需要同步更新插件
- 仓库根目录的 `go.mod` / `go.sum` 是**本地开发与 IDE 解析辅助**（gopls 消除框架包导入报错），安装与 CI 均不使用：`replace` 指向同级 `../AniaBot` 源码树（AniaBot 是主模块、无法作为版本化依赖拉取，该行是必需的）；clone 布局不同时改为指向你自己的 AniaBot 源码树路径即可

## 相关文档

- [AniaBot 插件系统概览](https://aniabot-project.github.io/AniaBot/plugin/overview) —— 插件如何被加载与执行
- [第一个插件](https://aniabot-project.github.io/AniaBot/plugin/first-plugin) / [完整教程](https://aniabot-project.github.io/AniaBot/plugin/tutorial) —— 从零开发插件
- [插件规范（本仓库）](docs/plugin-spec.md) —— plugin.json 元信息规范
- [AniaBot 文档站点](https://aniabot-project.github.io/AniaBot/) —— 全部文档

## 版本与兼容

- `plugin.json` 的 `version` 为插件版本；每次功能变更建议递增
- `min_framework` 声明最低 AniaBot 版本，低于该版本的 Bot 会拒绝安装
- `api_version` 为插件 API 兼容版本（当前 `1`），框架 API 不兼容升级时递增，旧插件会被标记不兼容
