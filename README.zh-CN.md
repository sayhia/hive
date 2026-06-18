<p align="center">
  <img src="build/appicon.png" width="116" alt="Hive 图标" />
</p>

<h1 align="center">Hive</h1>

<p align="center">把信息流滤成信号 —— 快速、本地优先的 RSS 阅读器。</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25" />
  <img src="https://img.shields.io/badge/Wails-v3%20alpha-d33?logo=wails&logoColor=white" alt="Wails v3" />
  <img src="https://img.shields.io/badge/Vue-3-42b883?logo=vuedotjs&logoColor=white" alt="Vue 3" />
  <img src="https://img.shields.io/badge/SQLite-FTS5-003B57?logo=sqlite&logoColor=white" alt="SQLite FTS5" />
  <img src="https://img.shields.io/badge/platform-macOS%20%C2%B7%20Linux-555?logo=apple" alt="macOS / Linux" />
</p>

<p align="center"><a href="README.md">English</a> · <strong>简体中文</strong></p>

---

Hive 是一款桌面 RSS 阅读器，所有数据都留在你自己的机器上。众多订阅汇入一个「蜂巢」——
文章、播客、YouTube、邮件订阅、社交时间线——统一存进一个本地 SQLite 数据库。
无需账号、不上云、不追踪。想写脚本时，还有一个输出 JSON 的命令行工具与应用共享同一个库。

## 功能

**订阅一切**
- RSS、Atom、JSON Feed，并能从页面 URL 自动发现订阅源
- 针对 YouTube、Reddit、Mastodon、Bluesky、播客、邮件订阅的差异化处理
- 用邮箱订阅：轮询 IMAP 邮箱，把 newsletter 变成一个源
- 内置源中心（Source Hub）：浏览多来源订阅目录、订阅前预览、一键批量订阅当前视图
- 支持 `rsshub://` 短链接、`hive://subscribe` 深链，OPML 导入 / 导出
- 按源设置刷新间隔；也可只刷新单个源或单个文件夹

**读得舒服**
- 干净的全屏阅读浮层，字体、字号、行距、栏宽可调——支持下载开源字体，
  中西文混排细节经过打磨
- 自选文章打开方式——RSS 正文、自动全文提取（readability）、或在原生网页视图中
  打开原文——全局默认 + 按源覆盖
- 全屏图片查看器：点开正文任意图片，← / → 翻阅全文图集
- 列表 / 卡片两种视图、悬停预览、预计阅读时长、空格翻页、专注模式
- 内置音频播放器，直接听播客与音频附件

**组织与检索**
- 文件夹、标签、智能视图（全部 / 未读 / 星标 / 稍后读）
- SQLite FTS5 全文搜索（Unicode 感知）
- 过滤规则：自动跳过、标已读或加星新抓取的文章
- 高亮与阅读笔记：多色批注本地保存，另有回顾面板集中翻阅

**翻译与增强（自带密钥）**
- 多引擎翻译——LLM、Google、DeepL、Bing——流式输出、保留 HTML 结构，
  支持原文 / 译文 / 双语对照三种视图
- 按源自动翻译；列表标题/摘要随滚动进入视口逐行翻译，带缓存，回滚免费
- AI 总结 / 问答 / 摘报，流式响应（Anthropic、OpenAI、DeepSeek，
  或任何 OpenAI 兼容端点——包括本地服务）

**同步与集成**
- 通过 Google Reader API 与 FreshRSS / Miniflux 双向同步——订阅列表双向收敛，
  已读/星标状态按全库对账，而不只是最近若干条
- macOS 菜单栏托盘实时显示未读数，支持通知、开机自启、单实例运行

**打造你自己的样子**
- 浅色 / 深色 / 跟随系统外观，三档深色纸面，金色以及冷色系强调色
  （azure · cyan · emerald · indigo · violet · slate）或任意自定义色，均在 OKLCH 色彩空间推导
- 命令面板（`⌘K`）、完整键盘映射、支持减弱动效
- 界面语言：English、日本語、简体中文

## hive-cli

整个库都可以脚本化。`hive-cli` 打开与应用相同的 SQLite 数据库（应用运行时也可安全使用），
每条命令输出一份 JSON——为 LLM agent 和 shell 管道而生。

```bash
task build:cli                 # → bin/hive-cli

hive-cli home                  # 计数 + 最新未读
hive-cli list -unread -limit 10
hive-cli search "borrow checker"
hive-cli read 42 -mark         # 全文正文，并标已读
hive-cli subscribe https://example.com/feed.xml
hive-cli refresh
```

完整参考见 [`cmd/hive-cli/README.md`](cmd/hive-cli/README.md)。

## 技术栈

**后端 — Go**
- [Wails 3](https://v3.wails.io/)（alpha）——原生窗口 + Go↔TS 绑定
- [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) ——纯 Go SQLite，带 FTS5（无 CGO）
- [`mmcdole/gofeed`](https://github.com/mmcdole/gofeed) —— RSS / Atom / JSON 解析
- [`PuerkitoBio/goquery`](https://github.com/PuerkitoBio/goquery) + [`microcosm-cc/bluemonday`](https://github.com/microcosm-cc/bluemonday) —— HTML 净化
- [`go-shiori/go-readability`](https://github.com/go-shiori/go-readability) ——全文提取
- [`emersion/go-imap`](https://github.com/emersion/go-imap) + [`go-message`](https://github.com/emersion/go-message) ——邮件订阅抓取
- `golang.org/x/{text,sync,net}` ——编码检测、信号量、HTML 分词

**前端 — Vue 3**
- `<script setup>` + TypeScript，[Pinia](https://pinia.vuejs.org/)，[TanStack Query](https://tanstack.com/query) 与 Virtual
- [vue-i18n](https://vue-i18n.intlify.dev/)、[marked](https://marked.js.org/)、`@wailsio/runtime`、Vite

## 架构

- **服务 → 绑定。** 每个领域（`FeedService`、`ArticleService`、`AIService`……）
  是 `services/` 下的一个 Go 结构体，由 `wails3 generate bindings` 自动导出为
  带类型的 TypeScript，前端像调用本地异步函数一样使用。
- **事件承载流式。** 长任务（AI token、刷新进度、翻译批次）按请求发独立的
  Wails 事件，前端用 `Events.On/Off` 订阅。流带序号戳，乱序投递不会打乱内容；
  错误统一为稳定的 `{ code, detail }` 形态。
- **本地数据层。** WAL 模式 SQLite + 只读连接池，`user_version` 迁移器，
  FTS5 搜索，以及自定义 `unicode_lower` 函数保证规则匹配的 Unicode 正确性。

```
hive/
├── main.go            # Wails 装配：服务、窗口、托盘、深链、单实例
├── cmd/hive-cli/      # 面向 agent 的命令行（JSON 输出），与应用共库
├── internal/          # 后端包
│   ├── db/            #   SQLite 数据层（迁移、查询、FTS5）
│   ├── models/        #   共享结构体（camelCase JSON ↔ TS 契约）
│   ├── ingestion/     #   抓取 + 解析订阅源，刷新调度器
│   ├── sources/       #   源 URL 归一化（YouTube/Reddit/Mastodon/…）
│   ├── discovery/     #   订阅目录检索 + 深链解析
│   ├── sanitize/      #   HTML 净化与文本提取
│   ├── extraction/    #   readability 全文 + 头图
│   ├── ai/            #   SSE 流式（Anthropic / OpenAI / DeepSeek）
│   ├── translate/     #   多引擎翻译
│   ├── syncer/        #   FreshRSS / Miniflux（Google Reader API）
│   ├── newsletter/    #   IMAP 邮件订阅抓取
│   ├── opml/          #   OPML 导入 / 导出
│   ├── fonts/         #   可下载的阅读/界面字体管线
│   ├── appstate/      #   共享状态（数据库 + HTTP 客户端 + 锁）
│   ├── events/        #   防崩溃、带序号的事件发送
│   └── apperr/        #   稳定错误码
├── services/          # Wails 服务（自动绑定为 TypeScript）
├── frontend/          # Vue 3 应用（组件、stores、composables、lib、locales）
└── build/             # Wails 配置、图标、各平台 Taskfile
```

## 上手

### 前置条件

- **Go ≥ 1.25**
- **Node.js + npm**
- **[Task](https://taskfile.dev/)**（`task` 命令）
- **Wails 3 CLI**：`go install github.com/wailsapp/wails/v3/cmd/wails3@latest`
  （运行 `wails3 doctor` 检查工具链）

### 开发

```bash
task dev        # 热重载运行（前端 + Go）
```

### 构建

```bash
task build      # 编译应用二进制        → bin/hive
task package    # 打包 macOS 应用       → bin/hive.app（图标 + ad-hoc 签名）
task build:cli  # 构建 agent 命令行     → bin/hive-cli
```

> Vue 前端通过 `go:embed` 嵌入 Go 二进制，打包出的应用完全自包含。
> Release 提供 macOS 与 Linux 产物；另有无界面的服务器模式
> （`task build:server`）和 Docker 镜像可在无 GUI 环境运行。

### 数据

你的整个库就是一个 SQLite 文件：

```
~/Library/Application Support/Hive/hive.db   # macOS
~/.config/Hive/hive.db                       # Linux
```

AI、翻译、同步、邮件订阅均为可选——在**设置**中填入对应密钥 / 凭据即可启用。

## 键盘快捷键

| 按键 | 动作 |
| --- | --- |
| `⌘K` / `/` | 命令面板 |
| `J` / `K` | 下一篇 / 上一篇 |
| `←` / `→` | 阅读器内切换上一篇 / 下一篇 |
| `Space` | 正文翻页 |
| `S` / `B` / `U` | 星标 · 稍后读 · 切换未读 |
| `C` | 复制文章链接 |
| `T` | 切换翻译 |
| `I` | 当前文章的 AI 助手 |
| `F` | 专注阅读模式 |
| `O` | 在浏览器打开原文 |
| `⇧A` | 全部标为已读 |
| `⌘R` | 刷新所有订阅源 |

完整映射见 **设置 → 快捷键**。

## 致谢

- Hive 构建于 [papr](https://github.com/l0ng-ai/papr)（作者
  [l0ng-ai](https://github.com/l0ng-ai)）的设计与数据模型之上——感谢原作。
- 字体：**Inter Tight**（Rasmus Andersson）· **Newsreader**（Production Type）· **JetBrains Mono**（JetBrains）
- 也感谢维护开放标准的每一个人——RSS 让独立写作始终可达。

## 许可证

[MIT](LICENSE) — © 2026 [l0ng-ai](https://github.com/l0ng-ai)（papr，原作）& sunmking（Hive）
