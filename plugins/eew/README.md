# 地震预警与气象速报 (EEW)

实时推送全国地震预警与中国地震台网速报，支持本地烈度估算、定时气象排行榜播报，具备 Cloudflare 403 自动降级与高可靠连接管理。

## 功能特性

- **多数据源毫秒级预警**：
  - 支持 **四川省地震局**（默认）、**中国地震台网**、**日本气象厅**、**福建省地震局**、**重庆市地震局**及**全量源**。
  - 基于 WebSocket 毫秒级广播推送地震预警信息。
- **Cloudflare 403 自动无缝降级**：
  - 连接模式提供 `auto`（默认）、`websocket`、`http_polling`。
  - 当 WebSocket 因 Cloudflare 拦截返回 403 时，自动无缝降级切换为 HTTP 高频轮询保底，确保预警不中断。
- **本地位置与预估烈度测算**：
  - 可配置本地经纬度与地名备注（如 `成都`、`北京海淀`）。
  - 基于中国地震烈度衰减关系模型（国家标准/汪素云中国大陆经验公式），自动测算**震中距**与**本地预估烈度**及震感分级（无感 / 轻微有感 / 明显震感 / 强烈震感 / 破坏性震感）。
- **全国气象排行榜定时播报**：
  - 支持配置 Cron 定时任务（如早、中、晚整点播报）。
  - 自动获取并播报全国每小时**最高温 Top 5**、**最大降水 Top 5**、**最大风速 Top 5**。
- **防打扰与灵活推送策略**：
  - 支持推送策略：仅首报、首尾报（默认）、全量报。
  - 支持按最小震级、最小烈度、地名关键词过滤。
  - 支持夜间免打扰时段，免打扰期间仅放行特定震级以上的特大地震。
  - 支持强震时 `@全体成员`。

## 指令说明

群聊需要先 @机器人（或私聊直接发送）：

| 指令 | 说明 | 示例 |
| --- | --- | --- |
| `/eew` 或 `/地震` | 查询中国地震台网最新 5 条地震速报目录（支持附带本地测算） | `/eew`、`@Bot /地震` |
| `/eew status` | 查看当前地震预警插件的连接状态、数据源与配置信息 | `/eew status` |
| `/weather` 或 `/天气` | 查询全国最新一小时气象实况排行榜（气温/降水/风速） | `/weather`、`@Bot /天气` |

## 配置项

安装后，进入 Web 控制台「配置管理」即可在图形化表单中修改配置，无需重启即可动态生效大部分设置：

### 基础设置
| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.eew.enable` | `true` | 是否启用地震预警插件 |
| `plugin.eew.source` | `sc_eew` | 预警数据源：`sc_eew`、`cenc_eew`、`jma_eew`、`fj_eew`、`cq_eew`、`all_eew` |
| `plugin.eew.connection_type` | `auto` | 连接模式：`auto` (优先 WS，被拦截自动切 HTTP 轮询)、`websocket`、`http_polling` |
| `plugin.eew.poll_interval` | `2` | HTTP 轮询间隔（秒） |
| `plugin.eew.max_age_minutes` | `10` | 历史预警过滤时限（分钟），超过此时间的旧预警静默忽略 |
| `plugin.eew.min_magnitude` | `3.0` | 最小推送震级，低于此值不推送 |
| `plugin.eew.min_intensity` | `0` | 最小震中预估烈度，0 表示不过滤 |
| `plugin.eew.push_strategy` | `first_and_final` | 推送策略：`first_and_final` (首报+末报)、`all` (全报)、`first_only` (仅首报) |
| `plugin.eew.groups` | `[]` | 接收预警推送的群 ID 列表（QQ 填纯群号或带 `qq:` 前缀） |
| `plugin.eew.friends` | `[]` | 接收预警推送的好友 ID 列表 |

### 地区关注与本地烈度测算
| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.eew.focus_mode` | `all_regions` | `all_regions` (推送全部地区) 或 `keyword_only` (仅关注地区) |
| `plugin.eew.focus_keywords` | `[]` | 关注地名列表，如 `["四川", "成都"]` |
| `plugin.eew.location_enable` | `false` | 是否开启本地测算（测算震中距与本地烈度） |
| `plugin.eew.location_name` | `本地` | 本地地名备注，如 `成都` |
| `plugin.eew.location_lat` | `0` | 本地纬度（北纬为正） |
| `plugin.eew.location_lng` | `0` | 本地经度（东经为正） |
| `plugin.eew.min_local_intensity` | `0` | 最小本地烈度过滤门槛（低于该烈度不推送） |

### 气象定时播报
| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.eew.weather_cron_enable` | `false` | 是否开启气象定时播报 |
| `plugin.eew.weather_cron` | `0 8,12,18 * * *` | 播报 Cron 表达式（默认每天 8:00、12:00、18:00） |
| `plugin.eew.weather_cron_groups` | `[]` | 接收气象播报的群列表 |
| `plugin.eew.weather_cron_friends` | `[]` | 接收气象播报的好友列表 |

### 强震提醒与夜间免打扰
| 配置键 | 默认值 | 说明 |
| --- | --- | --- |
| `plugin.eew.at_all` | `false` | 发生强震时在群内 @全体成员 |
| `plugin.eew.at_all_min_magnitude` | `5.5` | 触发 @全体成员 的最小震级 |
| `plugin.eew.quiet_hours_enable` | `false` | 开启夜间免打扰 |
| `plugin.eew.quiet_hours_start` | `23:00` | 免打扰开始时间 |
| `plugin.eew.quiet_hours_end` | `07:00` | 免打扰结束时间 |
| `plugin.eew.quiet_min_magnitude` | `5.0` | 免打扰时段依然放行的特大地震震级门槛 |

## 数据来源与鸣谢

- 实时地震预警与气象数据：[Wolfx Project](https://wolfx.jp/) 开放 API
- 地震速报目录：[中国地震台网 (CENC)](https://www.ceic.ac.cn/)

## 卸载说明

- 从插件市场卸载或 Bot 退出/重启时，会取消连接管理与配置监视任务、断开 WebSocket 并释放去重缓存，不留残留连接
