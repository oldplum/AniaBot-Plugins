// 本 go.mod 仅用于本地开发与 IDE（gopls）解析框架包，不参与插件安装：
// 市场安装流水线只解压 plugins/<id>/ 目录并编译进 AniaBot 主模块，
// CI（scripts/validate.sh）也在 AniaBot 源码树内编译，均不会读取本文件。
module github.com/jeanhua/AniaBot-Plugins

go 1.25.0

require (
	github.com/go-resty/resty/v2 v2.17.1
	github.com/gorilla/websocket v1.5.3
	github.com/jeanhua/AniaBot v0.0.0
	github.com/spf13/viper v1.21.0
	golang.org/x/net v0.57.0
)

require (
	github.com/anthropics/anthropic-sdk-go v1.33.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/modelcontextprotocol/go-sdk v1.4.0 // indirect
	github.com/openai/openai-go/v3 v3.59.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/sagikazarmark/locafero v0.11.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.3 // indirect
	github.com/sourcegraph/conc v0.3.1-0.20240121214520-5f936abd7ae8 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// AniaBot 是主模块（tag 为 v4.x 但 module path 无 /v4 后缀），不能作为
// 版本化依赖拉取，必须 replace 到同级源码树（推荐本地开发布局）：
// 把两个仓库克隆到同一父目录即可，IDE 会直接使用本地框架代码。
replace github.com/jeanhua/AniaBot => ../AniaBot
