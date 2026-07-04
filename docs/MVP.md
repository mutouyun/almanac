# 🚀 Almanac MVP 方案

> 本文档定义 Almanac 的 MVP（最小可行产品）阶段方案。
>
> **核心目的**：在投入真正的业务设计与编码之前，先用一个最小闭环验证「想法、效果、CI/CD 全链路」是否符合预期，把**基础设施**先搭稳。

---

## 1. 目标与非目标

### ✅ 本阶段目标（要验证什么）

- **技术链路打通**：Go 后端能编译、运行、对外提供 HTTP 服务。
- **前后端集成模式验证**：Astro 构建出静态产物，经 Go `embed` 打包进单一二进制，通过后端访问到页面。
- **CI/CD 全链路跑通**：代码 push 后自动构建、测试，并自动部署到服务器、重启服务。
- **单文件部署验证**：确认「一个二进制搞定前后端」的部署设想真实可行。

### 🚫 本阶段非目标（明确不做）

- 不做真实业务功能（博客 / 日记 / 记账的实际增删改查）。
- 不做数据库（SQLite）、数据模型、数据迁移。
- 不做完整登录鉴权体系（仅在需要时用最简手段占位）。
- 不做精美 UI / 完整前端页面（一个 Hello 页面即可）。
- 不引入重型 Web 框架（先用标准库，Gin/Echo 推迟到正式开发）。

> 一句话：**MVP 只验证「骨架能不能活、流水线能不能自动把它送上线」，不碰业务血肉。**

## 2. 验证清单（成功标准）

MVP 完成的判定以下列可勾选项为准：

- [ ] Go 服务能 `go build` 产出单一可执行文件，并在本地正常运行。
- [ ] 服务提供一个 `GET /health` 端点，返回 `200` 与简单状态信息（如 `{"status":"ok"}`）。
- [ ] 前端 Astro 能构建出静态产物（`web/dist`）。
- [ ] 静态产物通过 Go `embed` 打包进二进制，访问根路径能看到一个 Hello 页面。
- [ ] push 到 `main` 自动触发 CI：执行 `go build` + `go test`。
- [ ] CI 通过后自动触发 CD：将二进制部署到服务器并重启服务。
- [ ] 部署后从外部访问服务器上的服务，能看到同样的 Hello 页面与 `/health` 响应。

> 最关键的一条是**最后两项**：它们证明“代码一推，服务器上的东西就自动更新”——这正是基础设施阶段最想验证的能力。

## 3. MVP 技术栈范围

从 README 的完整选型里“砍”到最小，只保留验证链路必需的部分：

| 层 | MVP 选型 | 说明 |
|---|---|---|
| 后端 | Go 标准库 `net/http` | 先不上 Gin/Echo，减少依赖，验证最小服务 |
| 前端 | Astro（单个 Hello 页面） | 只验证构建 + embed 集成，不做真页面 |
| 集成 | Go `embed` | 前端 `dist` 打包进二进制 |
| CI/CD | GitHub Actions | build / test / deploy 全链路 |
| 部署 | SSH 推二进制 + 重启 | 向目标服务器交付 |

**推迟到正式开发阶段**（MVP 不碰）：SQLite 与数据模型、登录鉴权、Gin/Echo、业务 API、ECharts 看板。

## 4. MVP 目录落地

本阶段先填充以下文件（其余目录保留 `.gitkeep` 占位）：

```
almanac/
├── go.mod                     # go mod init github.com/mutouyun/almanac
├── cmd/almanac/
│   └── main.go                # 最小 HTTP 服务 + /health + embed 静态页
├── web/
│   ├── package.json           # Astro 工程
│   ├── astro.config.mjs
│   ├── src/pages/index.astro  # Hello 页面
│   └── dist/                  # 构建产物（CI 生成，不入版本）
└── .github/workflows/
    └── ci-cd.yml              # 构建 + 测试 + 部署流水线
```

## 5. 实施步骤（分阶段）

1. **最小 Go 服务**：`go mod init` → 写 `main.go`（`net/http` 起服务，`/health` 返回 200）→ `go run` 本地验证。
2. **前端 Hello 页**：初始化 Astro 工程 → 一个 `index.astro` → `npm run build` 产出 `dist`。
3. **embed 集成**：用 Go `embed` 把 `dist` 打进二进制，根路径托管静态页 → `go build` 后单文件能同时提供页面与 `/health`。
4. **CI**：`.github/workflows/ci-cd.yml` 加 build + test，push 到 main 自动触发。
5. **CD**：UT 通过后，通过 SCP 把编译好的二进制传到服务器 → 备份旧版 → 重启 Windows 服务 → `/health` 健康检查（失败则回滚）→ 外部访问验证。

## 6. CI/CD 设计

### 触发条件

- push 到 `main` 分支 → 跑完整 CI + CD。
- Pull Request → 只跑 CI（build + test），不部署。

### 阶段划分

1. **Build**：检出代码 → 装 Node 构建 Astro（产出 `web/dist`）→ 装 Go 编译（将 dist embed 进二进制）。
2. **Test（UT 质量闸门）**：`go test ./...`。测试在 GitHub Actions 的 runner（云端）上执行，**测试不过则不进入部署**。MVP 阶段可先放一个占位测试，验证闸门能跑。
3. **Deploy（仅 main，且 Test 通过后）**：将已验证的二进制推至服务器并重启服务（详见下文）。

> 说明：本项目无独立测试服务器。“测试”在云端 runner 完成，服务器只接收**已通过测试**的产物，直接正式部署。对个人项目而言这是合理且足够的权衡。

### 部署方式（SCP + SSH）

采用 **SCP 传文件 + SSH 执行重启**，在 GitHub Actions 中用现成 action（如 `appleboy/scp-action` 与 `appleboy/ssh-action`），无需手写大量命令。部署动作：

1. `scp` 将新编译的 `almanac.exe` 传到服务器目标目录。
2. `ssh` 登录服务器执行：备份旧版 → 替换新版 → 重启服务。

### Windows 服务化管理（关键）

目标服务器为 **Windows**，进程管理与 Linux 不同。将 almanac **注册为 Windows 服务**（推荐用 [NSSM](https://nssm.cc/)），带来：

- 开机自启、崩溃自动拉起。
- 部署重启动作统一为：`nssm restart almanac`（或 `sc stop/start`），比直接 kill 进程稳得多。

### 部署步骤（服务器端）

```
1. 停服务：       nssm stop almanac
2. 备份旧版：     almanac.exe -> almanac.exe.bak（用于回滚）
3. 替换新版：     scp 传入的 almanac.exe 覆盖
4. 起服务：       nssm start almanac
5. 健康检查：     请求 /health，非 200 则回滚 almanac.exe.bak 并重启
```

> 回滚兵器：部署前先备份旧二进制。新版起不来时能立即换回 `.bak`。个人项目无需蓝绿部署，一份备份足以兜底。

### 🔐 密钥与安全

- 部署所需凭据（SSH 私钥 / 主机 / 用户）**全部进 GitHub Secrets**，绝不硬编码入仓库。
- 服务器地址、用户名等敏感信息不出现在代码与文档中（保持 README 已做的“去 Windows/去具体环境”原则）。
- 部署脚本使用最小权限账号，避免直接用高权限用户。

## 7. 风险与安全注意

- ⚠️ **服务对外暴露**：MVP 阶段服务无鉴权，对公网开放前需谨慎。`/health` 与 Hello 页无敏感数据尚可，但后续接入真实数据/后台前，必须先有鉴权与最小防护。
- 部署端口建议先限定必要范围，配合防火墙规则。
- CI/CD 密钥泄露风险：定期轮换 SSH 密钥，Secrets 只给必要的 workflow 使用。

## 8. MVP 完成后的下一步

MVP 验证通过 = 基础设施就绪。之后才进入**正式设计与编码**：数据模型设计 → SQLite 接入 → 鉴权 → 逐模块开发（内容 / 记账）→ 前端真实页面与 ECharts 看板。到时可将本文档留作里程碑记录，并将详细设计另起 `docs/DESIGN.md`。
