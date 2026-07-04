# 📖 Almanac

> 🏡 自托管个人平台：博客、日记与记账本三合一，带后台内容管理与开销可视化看板，配套自动化 CI/CD 部署。
>
> A self-hosted personal platform for blogging, journaling, and expense tracking, with an admin dashboard and automated CI/CD.

Almanac 是一个跑在个人服务器上的一体化后端平台。它把「写点东西」和「记点账」这两件日常小事收拢到同一个地方：对外发布博客与日记，对内管理内容、记录并可视化日常开销。

---

## ✨ 需求定义

Almanac 要解决的核心诉求，是让一个人能在自己的服务器上，完整掌控自己的内容与数据。

### 内容发布

- 支持部署**动态与静态网页**，可用于个人博客、日记等内容展示。
- 内容以 Markdown 撰写，支持发布、编辑、归档。

### 记账本

- 记录日常支出，支持分类、时间维度的账目管理。
- 通过**可视化看板**呈现开销趋势与结构（按分类、按时间等维度）。

### 后台管理

- 需要**登录鉴权**才能进入后台。
- 后台可统一管理已发布的文字内容（博客 / 日记）。
- 后台可管理账目数据，并通过图表看板进行可视化分析。

### 部署与运维

- 部署在个人**服务器**上，具备**跨平台**能力（可适配不同操作系统环境）。
- 通过 **CI/CD 流水线**实现代码提交后的自动化构建与发布。

## 🛠️ 技术方案选型

### 后端：Go

选择 Go 作为后端语言，主要基于以下考量：

- **跨平台单文件部署**：`go build` 直接产出无依赖的单一可执行文件，丢到服务器即可运行，并可针对不同操作系统重新编译。
- **HTTP 能力强**：标准库 `net/http` 即可支撑后端服务，静态文件托管、API 路由都很成熟。
- **CI/CD 友好**：编译产物就一个二进制，流水线里 build → 传输 → 重启，干净利落。
- **低资源占用**：适合个人服务器长期常驻。

配套选型：

- **Web 框架**：[Gin](https://github.com/gin-gonic/gin) 或 [Echo](https://github.com/labstack/echo)（轻量、社区活跃）。
- **数据库**：[SQLite](https://www.sqlite.org/)（采用 `modernc.org/sqlite` 纯 Go 驱动，免 CGO，单文件数据库，零运维）。数据量增长后可平滑迁移至 PostgreSQL。
- **鉴权**：JWT 或 session cookie。

### 前端：Astro + 岛屿架构

前后端分离，前端为独立工程，通过 HTTP API 与 Go 后端通信。选择 [Astro](https://astro.build/) 的原因在于本项目三块功能「性格」差异明显：

- **博客 / 日记**：内容为主、交互少 → 走**静态生成（SSG）/ SSR**，首屏快、利于阅读与 SEO。
- **记账看板**：数据密集、交互多 → 用 **React / Vue 组件**做局部 SPA，配 [ECharts](https://echarts.apache.org/) 做图表可视化。

Astro 的**岛屿架构（Islands）**恰好把两者缝合：整站默认静态 HTML（几乎不加载 JS），只在记账看板等需要交互的局部嵌入框架组件并按需加载 JS。一套工程、一次构建，同时兼得「静态站的快」与「SPA 的强交互」。

### 前后端集成

前端构建产物可通过 Go 的 `embed` 包**打包进后端二进制**，最终仍是**单文件部署**，保持 CI/CD 流程简洁。

## 📁 仓库结构

> 以下为规划中的目标结构，会随开发逐步落地。

```
almanac/
├── cmd/
│   └── almanac/          # 主程序入口（main.go）
├── internal/             # 后端内部代码（不对外暴露）
│   ├── server/           # HTTP 服务器、路由、中间件
│   ├── handler/          # 各模块 API 处理器（博客 / 日记 / 记账 / 鉴权）
│   ├── service/          # 业务逻辑
│   ├── store/            # 数据访问层（SQLite）
│   └── model/            # 数据模型
├── web/                  # 前端工程（Astro）
│   ├── src/              # 页面、组件、岛屿
│   └── dist/             # 构建产物（由 Go embed 打包）
├── configs/              # 配置文件模板
├── scripts/              # 构建 / 部署辅助脚本
├── .github/
│   └── workflows/        # CI/CD 流水线定义
├── docs/                 # （预留）详细设计 / 架构决策文档
├── .gitignore
├── LICENSE
└── README.md
```

---

## 📄 License

[MIT](LICENSE) © mutouyun
