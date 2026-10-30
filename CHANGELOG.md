# 更新日志 / Changelog

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
- Git 仓库数据、个人配置及内容缓存保存在本机用户目录，不会打进应用安装包。资讯等联网内容需要网络，并会按页面显示的时间缓存和更新。

### 从旧版升级

版本比较从 v1.0.0 build 63 前进到 v1.1.0 build 64。GitLab 的 macOS v1.0.0 build 63 可使用应用内更新。Windows 旧版更新器仅替换 exe，无法添加本版所需的 `runtime` 目录；Windows 用户须下载 v1.1.0 ZIP 并完整解压，保留整个目录和 `runtime`。

GitHub 上此前的 v6.0.x 版本没有递增构建号，旧更新器无法将 1.1.0 判断为比 6.0.x 更新。请从 GitHub Releases 手动下载并安装一次 v1.1.0；之后使用新版本提供的更新流程。

## English

### v1.1.0 · build 64 · 2026-10-30

- **AI news and Skill discovery:** Added AIHOT news summaries, categories, source links, and related-Skill search. The discovery page highlights fast-rising GitHub projects over the recent week and newly discovered projects, with searches across skills.sh and skillsmp. These are candidates for discovery, not quality, security, or suitability certifications.
- **Models, calendar, and themes:** Added a model leaderboard and a Codex/Tibo reset calendar. Added a light theme and a light/dark switch, with clearer warning and conflict colors in light mode.
- **Local desktop app:** The package includes the services the app needs, portable Git, npm/npx JavaScript CLI, and pinned Skills CLI. Open the app directly; no terminal service needs to be started. Skill actions that run Node require Node.js 22.20.0 or later on the computer. If the version is missing or too old, the app offers the official download page when such an action is attempted. The Node executable is not bundled. News and local browsing do not require Node.
- **Local data:** Repository data, settings, and content caches stay in the user's local data directory and are not included in the app package. Online news needs a network connection and is cached according to the refresh time shown in the app.
- **Upgrade:** The release advances from v1.0.0 build 63 to v1.1.0 build 64. The GitLab macOS v1.0.0 build 63 app can update in-app. On Windows, the old updater replaces only the executable and cannot add this release's `runtime` directory; download and fully extract the v1.1.0 ZIP, keeping the entire directory. GitHub's previous v6.0.x releases have no monotonic release code, so the old updater cannot recognize 1.1.0 as newer than 6.0.x. Install v1.1.0 once from GitHub Releases; use the new version's update flow afterward.
