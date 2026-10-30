# 更新日志 / Changelog

## v1.1.0 · build 64 · 2026-10-30

### AI 资讯与 Skill 发现

- 增加 AIHOT 资讯页，可浏览资讯摘要、分类和来源链接，并根据资讯搜索相关 Skill。
- 增加发现页，突出近一周增长较快的 GitHub 项目和新发现项目，也可搜索 skills.sh 与 skillsmp。结果用于发现候选，不代表项目或 Skill 已经过质量、安全或适用性认证。

### 模型、日历与主题

- 增加模型榜单入口，集中查看当前提供的模型排名信息。
- 增加重置日历入口，查看 Codex/Tibo 相关的重置记录。
- 增加亮色主题和亮暗切换，并增强亮色主题下告警与冲突颜色的辨识度。

### 本地运行与依赖

- 桌面包包含应用运行所需服务、便携 Git、npm/npx 的 JavaScript CLI 和固定版 Skills CLI。双击打开 App 即可，不需要手动启动终端服务。
- Skill 安装等需要 Node 的操作使用本机 Node.js 22.20.0 或更新版本。Node 缺失或过旧时，只在尝试相关操作时提示官方下载入口；资讯和本地浏览不依赖 Node，Node 可执行程序也不包含在安装包中。
- 精简 macOS 运行时，省去可选的图形凭据管理器，同时保留 Git、Git LFS、应用内 HTTPS 凭据及已有 SSH 鉴权。
- 修复 Git 命令超时后，子进程继续占用输出管道、导致同步操作卡住的边界情况。
- 修正新版 Windows 更新器，使其后续更新时一并替换 exe 与 `runtime`；失败时尝试恢复更新前的文件。
- Git 仓库数据、个人配置及内容缓存保存在本机用户目录，不会打进安装包。资讯等联网内容需要网络，并按页面显示的更新时间缓存和更新。

### 从旧版升级

此版本为 v1.1.0 build64。GitHub 上此前的 v6.0.x 没有递增构建号，旧更新器不能把 1.x 判断为比 6.x 更新。请从 GitHub Releases 手动安装一次 v1.1.0；此后使用新版应用内更新流程。

Windows 旧更新器只替换 exe，无法加入本版需要的 `runtime` 文件夹。因此升级 Windows 旧版时，须下载完整 ZIP 并完整解压，保留整个目录和 `runtime`。升级到 v1.1.0 后，新版更新器会一并替换 exe 与 runtime。

## English

### v1.1.0 · build 64 · 2026-10-30

- **AI news and Skill discovery:** Added AIHOT news summaries, categories, source links, and related-Skill search. Discovery highlights GitHub projects with recent weekly growth and newly discovered projects, alongside skills.sh and skillsmp search. Results are discovery candidates, not quality, security, or suitability certifications.
- **Models, calendar, and themes:** Added a model leaderboard and a Codex/Tibo reset calendar. Added a light theme and a light/dark switch, with clearer warning and conflict colors in light mode.
- **Local desktop app:** The package includes the services the app needs, portable Git, npm/npx JavaScript CLI, and the pinned Skills CLI. Open the app directly; no terminal service needs to be started. Skill actions that run Node require Node.js 22.20.0 or later. If missing or too old, the app offers the official download page only when such an action is attempted. Node itself is not bundled; news and local browsing do not require Node.
- **Smaller macOS runtime:** Removed the optional graphical credential manager while retaining Git, Git LFS, in-app HTTPS credentials, and existing SSH authentication.
- **Git and Windows updates:** Added a wait limit for Git subprocesses that keep output pipes open after a timeout. The current Windows updater updates both the executable and `runtime`; it attempts to restore the previous files if updating fails.
- **Local data:** Repository data, settings, and content caches stay in the user's local data directory and are not included in the app package. Online content requires a network connection and is cached according to the refresh time shown in the app.
- **Upgrade:** GitHub's previous v6.0.x releases have no monotonic release code, so the old updater cannot recognize 1.1.0 as newer than 6.0.x. Install v1.1.0 once from GitHub Releases; use the new version's in-app update flow afterward. On Windows, the old updater only replaces the executable and cannot add `runtime`, so download and fully extract the ZIP, keeping the complete directory. Future updates from v1.1.0 include both the executable and runtime.
