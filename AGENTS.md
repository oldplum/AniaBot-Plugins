# AniaBot-Plugins · AI 开发速查手册

> 本文件是 AI 在本仓库开发插件的**唯一必读入口**，读完即可开工，**无需翻 AniaBot 源码**。
> 本仓库只放插件源码与元信息；框架代码在隔壁 `AniaBot` 仓库（`../AniaBot`）。

## 1. 仓库布局（先看懂在哪写代码）

```
plugins/<id>/       # 市场插件，一个目录 = 一个插件（会进 index.json / 市场列表）
  plugin.json       # 元信息（必填，格式见第 3 节）
  README.md         # 插件介绍：功能、命令、配置、注意事项（必填）
  *.go              # 源码，不带 go.mod（必填，包名任意）
examples/example/   # 官方最小骨架（plugin.go / plugin.json / README.md），新插件先抄它
docs/plugin-spec.md # plugin.json 字段规范（细节查它）
index.json          # 聚合索引，CI 自动生成，勿手改
scripts/validate.sh # 本地校验（元信息 + 可选编译），提交前必跑
go.mod / go.sum     # 仅给 IDE 解析框架包用，不参与安装与 CI，勿删勿改 replace
```

关键事实：

- 插件目录名 == `plugin.json` 的 `id`（`^[a-z0-9_-]{2,64}$`），安装后源码位于 AniaBot 源码树
  `custom/plugins/<id>`，import 路径固定为 `github.com/jeanhua/AniaBot/custom/plugins/<id>`。
- 插件**没有自己的 go.mod**，作为 AniaBot 主模块的内包编译；要第三方依赖直接 `import`，
  安装流水线的 `go mod tidy` 会自动拉进主模块（PR 里说明新增依赖即可）。
- 本仓库根 `go.mod` 有一行 `replace github.com/jeanhua/AniaBot => ../AniaBot`，
  所以本地推荐布局是两个仓库放同一父目录（`../AniaBot` + `../AniaBot-Plugins`），
  这样直接打开本仓库就能 `go build ./...` / `go test ./...` 且 IDE 不报错。

## 2. 最小插件骨架（抄 `examples/example`，5 分钟可运行）

```go
package example

import (
    "context"

    "github.com/jeanhua/AniaBot/common/bot"
    "github.com/jeanhua/AniaBot/common/model/command"
    "github.com/jeanhua/AniaBot/common/model/message"
    "github.com/jeanhua/AniaBot/common/msgchain"
    "github.com/jeanhua/AniaBot/common/plugin"
    "github.com/jeanhua/AniaBot/common/plugininfo"
)

// 嵌入 plugin.Meta 即获得 Plugin 接口的全部默认实现（返回 true/nil），只需覆盖用到的事件。
type ExamplePlugin struct {
    plugin.Meta
}

// 构造函数名须与 plugin.json 的 entry.constructor 一致（默认 NewPlugin），必须返回 *ExamplePlugin。
func NewPlugin() *ExamplePlugin {
    p := &ExamplePlugin{}
    p.Name = "示例插件"                              // 插件名，全局唯一（重复会导致框架 panic）
    p.HelpWords = "at 我发送 /example 触发问候"        // /help 显示的帮助语
    p.AdminOnly = false                              // true = 仅管理员触发（对其他人隐藏）
    p.ShowFor = plugininfo.ShowForGroup | plugininfo.ShowForFriend // 显示范围（见下）
    p.Author = "jeanhua"
    p.Version = "1.0.0"
    p.Order = plugin.LevelNormal                     // 执行顺序，越小越先（-1000 日志层 / 0 普通 / 1000 后置）
    // p.Platforms = []string{"qq"}                  // 只支持 QQ 才写；为空 = 全平台
    return p
}

// 群聊消息事件。返回 (是否继续传给后续插件, 错误)：处理完返回 false，不相关返回 true。
func (p *ExamplePlugin) OnGroupMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if !cmd.Mention || cmd.Name != "example" { // 群聊命令必须 @机器人（cmd.Mention）
        return true, nil
    }
    c := msgchain.Builder().Group()
    c.Text("Hello, AniaBot!")
    b.SendGroupMsg(msg.GroupId, c.Build())
    return false, nil
}

// 私聊消息事件。私聊不需要 @，只判 cmd.Name。
func (p *ExamplePlugin) OnFriendMsg(ctx context.Context, b bot.Bot, cmd command.Command, msg message.Message) (bool, error) {
    if cmd.Name != "example" {
        return true, nil
    }
    c := msgchain.Builder().Friend()
    c.Text("Hello, AniaBot!")
    b.SendFriendMsg(msg.Sender.UserId, c.Build())
    return false, nil
}
```

`ShowFor` 取值：`plugininfo.ShowForGroup`（群聊）| `plugininfo.ShowForFriend`（私聊）|
`plugininfo.ShowForNone`（隐藏，如拦截器类插件）；**必须设置**，否则框架拒绝加载。

## 3. `plugin.json`（必填字段别漏）

```json
{
  "id": "example",
  "name": "示例插件",
  "description": "一句话介绍，展示在市场列表",
  "author": "jeanhua",
  "version": "1.0.0",
  "platforms": ["qq", "feishu", "telegram", "discord", "qqofficial"],
  "tags": ["示例"],
  "api_version": 1,
  "min_framework": "4.6.0",
  "entry": { "constructor": "NewPlugin" },
  "icon": ""
}
```

- 必填：`id`（== 目录名）、`name`、`description`、`author`、`version`（建议 semver）。
- `platforms` 为空 = 全平台；只用 QQ 专属能力就写 `["qq"]`（能力见第 5 节）。
- `api_version` 当前填 `1`；`min_framework` 声明最低框架版本（如用到新 API 就抬高）。
- `entry.constructor` 缺省 `NewPlugin`；`icon` 只能是插件目录内的文件名（禁路径）。
- 完整规则见 `docs/plugin-spec.md`；`index.json` 与 README 插件列表表由 CI 生成，**勿手改**。

## 4. 消息与命令模型（事件函数的输入长这样）

### 4.1 `command.Command`（框架已解析好，直接用）

```go
type Command struct {
    Name    string   // 命令名：用户发 "/r 2d6" 则 Name="r"，Args=["2d6"]；非 / 开头则 Name=""
    Args    []string // 空格切分的参数
    Mention bool     // 是否 @了机器人（群聊命令必须同时满足 Mention && Name）
}
```

- 解析规则：只拼接 `text` 段、检测 `at` 段是否 at 到机器人；文本必须以 `/` 开头才是命令。
- **群聊**：`if !cmd.Mention || cmd.Name != "xxx"` → 不相关就 `return true, nil`。
  私聊要不要 @ 取决于产品语义，一般只判 `cmd.Name`（参考 `whitelist`：群聊判 Mention，私聊不判）。
- 裸指令（如骰娘的 `.r 2d6`，不 `/` 不 `@` 也能触发）是插件自己解析 `msg` 原文实现的，
  框架 `cmd` 里拿不到——需要才抄 `plugins/dicegirl` 的做法，不需要就别碰。

### 4.2 `message.Message`（常用字段）

| 字段 | 说明 |
| --- | --- |
| `msg.Message []OB11Segment` | OneBot v11 消息段（text/at/image/reply/face…），裸指令/富文本解析用它 |
| `msg.RawMessage` | 纯文本拼接（ at 信息已丢，只做关键词匹配时用） |
| `msg.GroupId` / `msg.Sender.UserId` | 群 ID / 发送者 ID（回复时用这两个） |
| `msg.MessageId` / `msg.MessageSeq` | 消息 ID（`Reply()`/取详情用）/ 序号 |
| `msg.Sender.Nickname` / `msg.Sender.Card` | 昵称 / 群名片（展示用，Card 为空回退 Nickname） |
| `msg.SelfId` | 机器人自身 ID |
| `msg.Platform` | 事件来源平台（`"qq"` 等，框架已按 `Platforms` 过滤过，一般不用判） |
| `msg.Time` | 秒级时间戳 |

- 取可读文本：`msg.FriendlyText(true)`（日志/调试用）；取纯输入文本自己拼 `text` 段
  （框架 `utils.ExtraMessageStr` 就是这么做的，插件里一般不需要，自己判 `cmd` 即可）。
- **QID 带平台前缀**：`message.QID` 是字符串，QQ 为 `qq:12345` 形式。
  构造：`message.FromString("12345")` / `message.FromUint64(123)`；比较/透传直接用 `==`；
  只有在调外部 API 需要裸 QQ 号时才 `qid.TrimQQPrefix()`。其他平台前缀示例：`qo:`/`fs:`/`tg:`/`dc:`。

## 5. 发消息与平台能力（`bot.Bot` + `msgchain` + `bot.QQ`）

### 5.1 通用发送（全平台，优先用这个）

```go
// 群聊
c := msgchain.Builder().Group()
c.Text("hello ").Mention(msg.Sender.UserId).Face(14)
c.ImageUrl("https://...").Reply(msg.MessageId)
b.SendGroupMsg(msg.GroupId, c.Build())

// 私聊（注意 Friend 构造器 + SendFriendMsg 配对，别混用 Group/Friend）
c2 := msgchain.Builder().Friend()
c2.Text("hello")
b.SendFriendMsg(msg.Sender.UserId, c2.Build())
```

`msgchain` 常用段：`Text / Mention(qid) / Face(id) / ImageUrl / ImageBase64 / ImageLocal(path) /
VideoUrl / VideoLocal / FileUrl(name,url) / FileLocal / RecordUrl / RecordLocal / Reply(msgId) / Raw(seg...)`，
最后 `.Build()`。`Send*` 返回 `(msgId, success)`，失败判 `success` 并记日志。

### 5.2 QQ 专属能力（类型断言探测，失败就降级）

```go
if qb, ok := b.(bot.QQ); ok {
    // 合并转发（防撤回 /explore 就是这么发的）
    f := msgchain.Builder().GroupForward()
    f.Message(userId, nickname, someGroupChain)
    qb.SendGroupForwardMsg(msg.GroupId, f.Build())
    qb.SendPokeMsg(userId, &msg.GroupId) // 戳一戳
    qb.GetNCrkey()                        // rkey（给图片/文件 URL 补签名用，见 antiwithdrawal）
} else {
    // 非 QQ 平台：降级为普通文本回复
}
```

`bot.QQ` 还有：`SendFriendForwardMsg / SetMsgEmojiLike / SendGroupSign /
GetForwardMsg / GetGroupUserInfo / GetGroupMemberList / GetFriendList / GetGroupList / GetAIChatacter / GetPrivateFileURL`。
原则：**用到 QQ 专属能力 → `plugin.json` 的 `platforms` 写 `["qq"]`**（`antiwithdrawal` 范例）；
**只用 5.1 通用能力 → `platforms` 全平台**（`dicegirl`/`eew` 范例）。

### 5.3 读消息 / 通讯录（通用）

`b.GetMsgDetail(msgId)`、`b.GetGroupDetail(groupId)`、
`b.GetGroupMsgHistory(groupId, count, seq)`、`b.GetFriendMsgHistory(userId, count, seq)`
均返回 `(value, success)`。流式回复（先发后改）是可选接口 `bot.StreamSender`，
QQ 不支持——除非确定目标平台支持，否则别用。

## 6. 插件元信息与执行顺序（`plugin.Meta` 字段一次讲清）

| 字段 | 说明 |
| --- | --- |
| `Name` | 全局唯一，重复加载直接 panic；`/help` 与面板列表展示它 |
| `HelpWords` | `/help` 显示的帮助语，一句话说清触发方式 |
| `AdminOnly` | 仅管理员触发（对其他人隐藏） |
| `ShowFor` | Group / Friend / None（None = 拦截器类，后台插件） |
| `Author` / `Version` | 建议与 `plugin.json` 一致（面板已安装列表以此展示） |
| `Order` | 越小越先：`plugin.LevelLog(-1000)` 日志层、`plugin.LevelNormal(0)` 普通、`plugin.LevelPostHandle(1000)` 后置。拦截器要抢跑就设小（如 `whitelist` 用 `LevelLog+1` 拦住全部功能插件） |
| `Platforms` | 声明支持平台，空 = 全平台；框架按事件来源平台**自动过滤**，收到的事件一定是支持的平台 |

事件分发语义（心中默念）：插件按 `Order` 从小到大依次收到事件；`return false` 截断
（后续插件收不到）；`return true` 放行；**panic 不会截断**（框架捕获记日志后视为放行）。
自己发的消息不会回流成事件（框架过滤 `Sender == SelfId`）。单条消息处理默认 5 分钟超时
（`ctx` 会取消，`bot.msg_event_timeout_sec` 可配）——长任务用 `StartCron`/goroutine，别卡事件。

## 7. 生命周期与注入（按调用顺序）

```
ConfigSchema() → DI 注入 → Start → StartCron → Awake → 事件循环（OnGroupMsg/OnFriendMsg/Notice…）
```

- `Start(ctx, cfg *viper.Viper)`：初始化（建集合、读配置、起后台 goroutine 如 eew 的推送循环）。
- `StartCron(ctx, b, c)`：注册定时任务，`c.AddFunc("0 8 * * *", func(){...})`（cron 表达式；
  表达式非法会返回 err，记日志并返回）。`b` 可在闭包里发消息（eew 范例）。
- `Awake(ctx, b)`：全部插件 `Start` 完成后调用，适合打“就绪”日志/依赖别插件数据的初始化
  （`whitelist` 必须在 `Awake` 读拦截器名单——`Start` 按 Order 执行时对方还没初始化）。
- 注入字段（框架 DI 自动填，开箱即用）：
  - `p.Logger *slog.Logger`：结构化日志，`p.Logger.Info("...", "key", val)`。
  - `p.Storage`：缓存 KV + 队列（memory/redis），支持 TTL：`SetString(ctx,k,v)` /
    `GetString` / `Expire(ctx,k,ttl)` / `LPush/LRange/...`，`Clone("ns:")` 开命名空间。
  - `p.PersistentStorage`：持久 KV（sqlite/mysql，重启不丢）：`Get/Set/Has/Del/Keys/Clear`，
    `Clone("digest:")` 隔离自家 key。需要关系表时用 `storage.SQLBackend(store)` 探测
    `SQLPersistentStorage`，`storage.EnsureTables(...)` 建 `ania_` 前缀表——**探测/建表失败只记日志降级，
    必须留 KV 兜底，绝不能阻塞 `Start`**。
  - `p.RestyClient *resty.Client`：发 HTTP 优先用它（统一超时/代理），别 `http.DefaultClient`。
  - `p.SystemConfig.AdminId`：管理员 ID。管理员鉴权手写：
    `if msg.Sender.UserId != p.SystemConfig.AdminId { 回一句"仅管理员可用"; return false, nil }`
    （`whitelist` 范例；注意**拦截器要给管理员留门**，否则配错名单把自己锁死）。
  - `p.ConfigEditor`：读写框架配置中心（点分键，落 DB，重启生效），**可能为 nil，用前判空**。
    普通插件读配置不需要它（第 8 节的 `ConfigSchema` 已够用），只有改别插件配置的插件（如 `whitelist`）才用。

## 8. 配置（`ConfigSchema`，面板表单自动生成，优先用它）

```go
type myConfig struct {
    Enable   bool   `cfg:"plugin.myid.enable" label:"启用" group:"基础设置" default:"true" help:"关闭后不响应任何命令"`
    Mode     string `cfg:"plugin.myid.mode" label:"模式" type:"select" options:"a,b,c" group:"基础设置" default:"a"`
    MaxN     int    `cfg:"plugin.myid.max_n" label:"上限" group:"基础设置" default:"100"`
    Token    string `cfg:"plugin.myid.token" label:"令牌" type:"password" sensitive:"true" group:"密钥"`
    Prompt   string `cfg:"plugin.myid.prompt" label:"提示词" type:"text" group:"高级设置"`
    Groups   []string `cfg:"plugin.myid.groups" label:"作用群" type:"strings" group:"范围" help:"逗号分隔的群号"`
}
func (p *MyPlugin) ConfigSchema() any { return &p.cfg } // 必须每次返回同一个指针
```

- 框架启动时：反射 `cfg` 标签注册字段 → 缺失键写入默认值 → 面板按 `label/group/help` 渲染表单
  → `Start` 之前把值填进结构体，插件只读 `p.cfg` 字段即可（`dicegirl`/`eew` 范例）。
- tag 说明：`cfg`（点分键，统一 `plugin.<id>.*`，小写）、`label`（表单名）、
  `group`（表单分组，**一个插件只建一个 group、命名用插件名**，如 setu 全部 `group:"涩图"`、
  eew 全部 `group:"地震预警"`——面板「配置管理」侧边栏按 group 聚合成目录项，拆成
  "网络/展示/限流"等多个组会把侧边栏撑出一串碎条目）、
  `help`（说明）、`default`（默认值）、`type`（`string/password/int/float/bool/text/strings/ints/select`，
  不写按 Go 类型推断）、`options`（select 候选，逗号分隔）、`sensitive`（敏感字段面板掩码）。
  Go 指针标量 = 可选字段（未配置保持 nil）。
- **铁律**：`ConfigSchema()` 在 DI 之前调用，**不许用 `p.Logger/p.Storage` 等注入字段**，
  必须每次返回同一个指针（`return &p.cfg`）。键相同后注册覆盖先注册——别复用别插件的键前缀。

## 9. 通知事件与扩展接口（用到才实现，`Meta` 已给空实现）

- 群/好友通知（方法只返回 `error`，广播制、不截断）：`OnGroupRecall/OnFriendRecall`（防撤回）、
  `OnGroupIncrease/OnGroupDecrease`（入群欢迎/退群）、`OnGroupBan/OnGroupAdmin/OnGroupUpload/
  OnFriendAdd/OnPoke/OnLuckyKing/OnHonor/OnGroupMsgEmojiLike/OnEssence/OnGroupCard`。
  参数结构体都在 `common/model/message`（如 `message.GroupRecallNotice`）。
- `OnPanic(ctx, b, name, err)`：本插件 panic 后的回调（打日志/告警用，别指望恢复现场）。
- `OnPlatformEvent(ctx, b, event)`（实现 `plugin.PlatformEventHandler` 接口）：
  收平台自有事件（飞书卡片回调等，`event.Data` 类型由适配器定，自行断言），经 `Platforms` 过滤后广播。
- 后台推送型插件（eew 地震推送 / groupdigest 群刊）：`Start` 里起 goroutine + `context` 取消，
  或 `StartCron` 注册定时任务；推送目标群列表放配置里（`Groups []string` + `normalizeGroupIDs` 思路抄 `groupdigest`）。

## 10. 本地开发与校验（标准四步）

```bash
# 0. 布局：两仓库放同一父目录（`../AniaBot` 供 go.mod 的 replace 指向）
#    parent/AniaBot  parent/AniaBot-Plugins

# 1. 写代码（先抄 examples/example，再按需抄：配置→dicegirl/whitelist，定时→eew，
#    合并转发→antiwithdrawal，拦截器→whitelist，后台推送→eew/groupdigest）

# 2. 联调：复制到 AniaBot 源码树，生成注册代码并运行
cp -r plugins/<id> ../AniaBot/custom/plugins/<id>
cd ../AniaBot && go run ./tools/plugingen && go run cmd/main.go

# 3. 校验（与 CI 同一套；带 ANIA_SRC 会额外编译全部插件，提交前必跑）
cd ../AniaBot-Plugins && ANIA_SRC=../AniaBot bash scripts/validate.sh

# 4. 单测：纯逻辑抽出来写表驱动测试（抄 dicegirl/plugin_test.go），bot 相关只测函数不启动框架
go test ./plugins/<id>/...
```

`validate.sh` 会检查：`id==目录名` 且合法、`name/description/author/version` 非空、
有 `README.md` 和 `.go` 源码。`index.json` / README 插件列表表由 CI 在合 main 后自动同步——
**本地跑完校验若提示这两个文件有差异，无需处理**（`ALLOW_AUTOGEN_DIFF=1` 或等 CI）。

## 11. 红线（审查必挂项，先自查）

1. `init()` 里**禁止**网络请求、读写文件、起进程等副作用（安装编译期会被执行）。
2. 不窃凭据：不得读取 `data/`、环境变量密钥并外发；网络请求只发往合理目标，
   作者自己的服务必须在 README 说明用途，且**默认不启用**。
3. 文件读写限定插件自有命名空间（`Storage.Clone("ns:")`）或 `data/` 下安全位置。
4. 不执行任意 shell；确有需要必须默认关闭 + 管理员确认。
5. 第三方依赖越少越好、来源可信，PR 里说明新增依赖。
6. `Meta.Name` 全局唯一；`ShowFor` 必须设置；构造函数签名 `func NewPlugin() *XxxPlugin`。
7. README 写清：功能、全部命令（含是否要 @）、权限要求、配置项、注意事项。
8. 每次功能变更递增 `plugin.json` 的 `version`；用到新框架 API 就抬高 `min_framework`。
9. Go 版本 1.25+；注释与用户可见文案用中文；新增代码用 `slog`（`p.Logger`）记日志。

## 12. 提交流程

1. Fork → 新建分支 `plugin/<id>` → 在 `plugins/<id>/` 下放 `plugin.json` + `README.md` + 源码。
2. `ANIA_SRC=../AniaBot bash scripts/validate.sh` 通过。
3. 提 PR，标题格式：`plugin: 新增 <插件名> (<id>)` 或 `plugin: 更新 <插件名> (<id>)`。
4. CI 自动校验 + 维护者人工审查（审查清单见 `CONTRIBUTING.md`）→ 合 main 后 CI 自动更新
   `index.json` 与 README 插件列表。

## 13. 查文档地图（按需跳转，不用通读源码）

| 想查 | 看这里 |
| --- | --- |
| 最小骨架 | `examples/example/plugin.go`（本仓库） |
| `plugin.json` 字段 | `docs/plugin-spec.md`（本仓库） |
| 贡献/审查清单 | `CONTRIBUTING.md`（本仓库） |
| 配置表单写法 | `plugins/dicegirl/plugin.go` + `plugins/whitelist/config.go` |
| 定时任务 | `plugins/eew/plugin.go`（`StartCron`） |
| 合并转发 / QQ 专属 | `plugins/antiwithdrawal/plugin.go` |
| 拦截器 / 管理员鉴权 / Order 抢跑 | `plugins/whitelist/plugin.go` |
| 后台推送 / 作用群配置 | `plugins/eew`、`plugins/groupdigest` |
| 单测写法 | `plugins/dicegirl/plugin_test.go` |
| 框架教程 | 插件系统概览 / 第一个插件 / 完整教程（见 README 相关链接） |

## 14. 常见坑（AI 高频犯错点）

- 群聊回 `true` 还是 `false`：命令命中并回复了 → `false`（截断，避免 AI 插件又接一嘴）；
  不相关 → `true`。私聊同理。
- 群聊命令漏判 `cmd.Mention`：群里不 @ 就能触发是 bug（除非裸指令设计）；私聊别判 Mention。
- `Group()`/`Friend()` 构造器与 `SendGroupMsg`/`SendFriendMsg` 必须配对，混用编译不过。
- 私聊回复目标是 `msg.Sender.UserId`，群聊是 `msg.GroupId`；群聊想 @ 对方用 `c.Mention(...)`。
- `QID` 直接 `==` 比较；`message.FromString("123")` 会自动加 `qq:` 前缀，别手拼字符串。
- `ConfigSchema` 返回 `&p.cfg` 同一指针；`Start` 里读 `p.cfg`，别再手写 `cfg.Get*`。
- config 的 `group` 一个插件只能有一个、名字用插件名：侧边栏按 group 聚合，按主题拆组
  （"登录/网络/限流…"）会在「配置管理」侧边栏产生一堆碎条目，与其他插件风格不一致。
- 拦截器插件 `Order` 设小值抢跑，但 `Start` 里读别插件的数据可能为空——放 `Awake` 里读。
- 图片/文件 URL 3 分钟过期（rkey 签名），缓存消息重发要处理过期（抄 `antiwithdrawal`）。
- 长耗时（下载/AI/轮询）别卡在 `OnGroupMsg` 里超过 5 分钟；常驻后台任务在 `Start` 起 goroutine，
  退出时用 `ctx` 取消，别泄漏。
