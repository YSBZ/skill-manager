# SkillManager v1.1.0 桌面版（macOS）

通用版，支持 macOS 13.5 或更新版本，以及 Apple 芯片与 Intel Mac。Git、npm/npx 的 JavaScript CLI 和固定版 Skills CLI 随 App 提供；使用 Node 功能时需要本机安装 Node.js 22.20.0 或更新版本。无需另外启动终端服务。

## 安装

- **dmg**：双击打开，把 **SkillManager.app** 拖到「应用程序」（Applications）。
- **zip**：完整解压后双击 **SkillManager.app** 即可，也可拖进「应用程序」以便以后启动。

Git 与 npm/Skills CLI 文件保存在 App 内，缓存和配置写入用户目录，直接从解压目录运行也不需要终端。缺少合适的本机 Node.js 不影响 App 启动、资讯、日历、模型榜和本地内容查看。

## 首次打开

此版本使用本地 ad-hoc 签名，未经过 Apple 公证。若 macOS 首次拦截，请确认下载来源及文件校验值后，按系统提示进入「系统设置 → 隐私与安全性」，为 **SkillManager** 确认「仍要打开」。操作说明见 [Apple 官方文档](https://support.apple.com/en-us/102445)。

完成首次放行后，可正常双击启动。

## 使用

- 左侧管理 Git 仓、本地目录和 Skills CLI 来源；右侧按目标目录查看、启用或停用 Skills。
- App 使用随包 Git 2.56.0、npm 11.20.0 JavaScript CLI 和 Skills CLI 1.7.4；在线 Skill 安装等 Node 功能使用本机 Node.js 22.20.0 或更新版本。若缺少或版本过旧，相关操作会显示 Node.js 官方下载入口；安装后重新打开 App。App 不修改系统 PATH、用户 HOME 或全局软件安装。
- 远端仓库与在线 Skill 搜索、安装需要网络；本地已有内容可以离线查看。私有 Git 仓继续使用你的 Git 凭据和 SSH key。
- **本次更新**：增加 AIHOT 资讯摘要、来源和相关 Skill 搜索；发现页展示近一周增长较快的 GitHub 项目、新发现项目及技能市场搜索；加入模型榜、重置日历，以及亮暗主题和更清晰的告警/冲突颜色。发现结果是候选信息，不是质量认证。
- 资讯、重置日历和模型榜会缓存到本机；联网使用时，以页面展示的更新时间判断缓存新旧。
- 右上角「同步本地」重新扫描本地技能，Git 仓库也可在来源区域更新。
- App 内含运行所需服务，双击即可打开，无需手动启动服务。Git、npm 的 JavaScript CLI 和 Skills CLI 随包提供；执行安装等 Node 操作要求本机 Node.js 22.20.0 或更新版本。缺少或过旧时，应用只在相关操作中提示官方下载页；资讯和本地浏览不依赖 Node。
- Skill 源、设置和资讯缓存保存在本机 `~/.skillmanage/`，不会写进或随 App 包分发；更换/升级 App 不会打包这些个人数据。
- 从 GitLab v1.0.0 build63 可在 macOS App 内升级到 v1.1.0 build64。GitHub 旧版 v6.0.x 无法自动识别此次跨版本号变化，请先从 [GitHub Releases](https://github.com/YSBZ/skill-manager/releases/latest) 手动安装 v1.1.0；之后按新版应用内提示更新。

## 卸载

退出 App 后删除 `/Applications/SkillManager.app`。如不再需要本地配置，再删除 `~/.skillmanage/`。
