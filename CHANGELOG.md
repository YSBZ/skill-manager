# 更新日志 / Changelog

## v1.1.2 · build 66 · 2026-11-13

- “我的技能”保留断链和目标缺少 SKILL.md 的异常条目，展示入口、链接目标和真实来源。
- 分开展示安装／启用状态与本地检查结果，识别明确的 Skill 转发依赖、递归缺失和循环，并提示客户端工具映射或适配问题。
- 检查按当前目标、继承目录及适用作用域插件判断；其他项目的安装和仅收藏在源中的 Skill 不算当前可发现。
- 缺失依赖可进入现有搜索并显式启用；重新检查只读文件，不执行 Skill 命令，也不自动改写第三方 Skill。
- 使用 CommonMark 解析排除代码示例。本地检查不等于实际运行验证；动态工具和 MCP 可用性仍需在 Agent 会话中确认。
- 将“新项目发现”调整为“最新发现”，按首次收录时间排列，取消仓库必须创建于 30 天内和必须具备完整周增长数据的门槛；卡片区分收录与创建时间。
- GitHub 增长请求受限时保留已验证的项目资料，继续显示可发现的项目；本周飙升仍要求真实完整周数据，Skill 安装验证规则不变。
- 修复发现页空列表、加载及错误状态横向撑满的问题，保留左栏宽度和右侧留白；补充清除筛选、场景探索等入口，并准确展示请求受限及预计重试状态。
- 整理发现页筛选区：搜索与场景固定两行排列，技能市场入口使用次要按钮，避免窄窗口折成三行并挤压输入框。

v1.1.0 / v1.1.1 用户可按所用渠道的应用内提示升级。更早的 Windows 版本以及 GitHub 历史 v6.0.x 请手动下载完整 ZIP；Windows 须保留 `runtime` 目录。GitHub 请求受限时仍显示真实缓存，不代表上游采集已经恢复。

**English:** Fixes empty discovery lists by showing projects in first-discovered order without requiring a repository created in the last 30 days or complete weekly growth data. Keeps verified project metadata when optional growth requests fail, preserves strict weekly-ranking and Skill-install checks, and uses a compact two-row filter layout with clear empty states. Adds read-only local checks for broken Skill links and explicit Skill dependencies; these checks do not execute Skills or certify runtime/MCP availability. Existing v1.1.0/v1.1.1 users can use their channel's in-app update; legacy Windows and GitHub v6.0.x users should install the full archive manually.

## v1.1.1 · build 65 · 2026-11-02

- 修复启动时资讯列表停留在旧缓存的问题：本地服务抓到新内容后，尚未开始阅读且列表位于顶部时自动显示新列表。
- 旧缓存期间每15秒检查本地内容服务；缓存恢复新鲜后，仍按原来的5分钟周期检查，AIHOT抓取周期未变。
- 阅读文章或已滚动列表时继续保留当前位置，通过“有新内容”确认更新。
- v1.1.0用户可使用应用内更新。更早的Windows版本及GitHub历史v6.0.x仍需手动完整安装一次；Windows保留runtime目录。

**English:** Fixes news staying on an old startup cache after new data has arrived. An idle list at the top now adopts the fresh feed automatically. Stale local-cache checks retry every15 seconds, returning to the usual five-minute check interval when fresh; upstream polling is unchanged. Active readers retain their position and accept updates explicitly.

## v1.1.0 · build 64 · 2026-10-30

### AI 资讯与 Skill 发现

- 增加 AIHOT 资讯页：浏览资讯摘要、分类和来源链接，并从资讯中搜索相关 Skill。
- 增加发现页，展示近一周增长较快的 GitHub 项目和新发现项目，并可搜索 skills.sh 与 skillsmp。榜单用于发现候选，不代表项目或 Skill 已经过质量、安全或适用性认证。

### 模型、日历与主题

- 增加模型榜单入口，集中查看当前提供的模型排名信息。
- 增加重置日历入口，查看 Codex/Tibo 相关的重置记录。
- 增加亮色主题，并提供亮暗切换；亮色下增强告警与冲突颜色的辨识度。

### 本地运行与依赖

- 桌面包内含应用运行所需服务、便携 Git、npm/npx 的 JavaScript CLI 和固定版 Skills CLI。双击打开 App 即可，不需要手动启动终端服务。
- 为 Skill 安装等需要运行 Node 的操作，使用电脑上已安装的 Node.js 22.20.0 或更高版本。Node 缺失或过旧时，只在尝试相关操作时提示官方下载入口；资讯与本地内容浏览不依赖 Node。Node 可执行程序不包含在安装包中。
- 修正新版 Windows 更新器：将 exe 与完整 runtime 一并更新，先在安装盘暂存，失败时尝试恢复并保留无法恢复的备份。旧版用户首次迁移仍须完整解压安装。
- 修复 Git 命令超时后，子进程仍占用输出管道时同步操作可能继续卡住的边界情况。
- Mac 包省去可选的 .NET 图形 Git 凭据组件，保留 Git、Git LFS、应用内 HTTPS 凭据及已有 SSH 鉴权，减小通用安装包。
- Git 仓库数据、个人配置及内容缓存保存在本机用户目录，不会打进应用安装包。资讯等联网内容需要网络，并会按页面显示的时间缓存和更新。

### 从旧版升级

版本比较从 v1.0.0 build 63 前进到 v1.1.0 build 64。GitLab 的 macOS v1.0.0 build 63 可使用应用内更新。Windows 旧版更新器仅替换 exe，无法添加本版所需的 `runtime` 目录；Windows 用户须下载 v1.1.0 ZIP 并完整解压，保留整个目录和 `runtime`。

GitHub 上此前的 v6.0.x 版本没有递增构建号，旧更新器无法将 1.1.0 判断为比 6.0.x 更新。请从使用渠道的 Releases 页面手动下载并完整安装一次 v1.1.0；之后使用新版本提供的更新流程。GitLab 与 GitHub 分别维护自己的安装包和更新清单。

## English

### v1.1.0 · build 64 · 2026-10-30

- **AI news and Skill discovery:** Added AIHOT news summaries, categories, source links, and related-Skill search. The discovery page highlights fast-rising GitHub projects over the recent week and newly discovered projects, with searches across skills.sh and skillsmp. These are candidates for discovery, not quality, security, or suitability certifications.
- **Models, calendar, and themes:** Added a model leaderboard and a Codex/Tibo reset calendar. Added a light theme and a light/dark switch, with clearer warning and conflict colors in light mode.
- **Local desktop app:** The package includes the services the app needs, portable Git, npm/npx JavaScript CLI, and pinned Skills CLI. Open the app directly; no terminal service needs to be started. Skill actions that run Node require Node.js 22.20.0 or later on the computer. If the version is missing or too old, the app offers the official download page when such an action is attempted. The Node executable is not bundled. News and local browsing do not require Node.
- **Local data:** Repository data, settings, and content caches stay in the user's local data directory and are not included in the app package. Online news needs a network connection and is cached according to the refresh time shown in the app.
- **Upgrade:** The release advances from v1.0.0 build 63 to v1.1.0 build 64. The GitLab macOS v1.0.0 build 63 app can update in-app. On Windows, the old updater replaces only the executable and cannot add this release's `runtime` directory; download and fully extract the v1.1.0 ZIP, keeping the entire directory. GitHub's previous v6.0.x releases have no monotonic release code, so the old updater cannot recognize 1.1.0 as newer than 6.0.x. Install v1.1.0 once from the Releases page for your channel; use the new version's update flow afterward. GitLab and GitHub each maintain their own desktop packages and update manifest.
