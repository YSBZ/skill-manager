# SkillManager v1.1.0 桌面版（Windows）

原生窗口版，双击运行。压缩包包含 Git 2.56.0、npm/npx 的 JavaScript CLI 和固定版 Skills CLI；使用 Node 功能时需要本机安装 Node.js 22.20.0 或更新版本。无需手动启动服务。

## 安装

1. 将压缩包解压到普通可写目录，例如 `D:\Tools\SkillManager\`。
2. 双击 **SkillManager.exe**。

必须保留完整解压目录，包括 `runtime` 文件夹；不要只复制 exe。Git、npm 的 JavaScript CLI 和 Skills CLI 都在此目录中。

不要把程序放到 `Program Files` 或压缩软件的临时解压目录；自更新需要写入程序目录。

## 首次打开

程序未经代码签名。首次运行时 SmartScreen 可能提示「Windows 已保护你的电脑」；请确认下载来源及文件校验值后，再选择「更多信息」→「仍要运行」。

## WebView2

WebView2 已包含在 Windows 11 中，也已安装在大多数 Windows 10 设备上。如果系统没有合适版本，SkillManager 会提示你确认，然后运行随程序内嵌的微软 Evergreen 引导程序。这个过程需要联网；无需另行下载 WebView2。安装后的 Evergreen Runtime 由微软自动更新。

## 使用与数据

- App 使用压缩包内的 Git、npm JavaScript CLI 和 Skills CLI；在线 Skill 安装等 Node 功能使用本机 Node.js 22.20.0 或更新版本。若缺少或版本过旧，相关操作会显示 Node.js 官方下载入口；安装后重新打开 App。App 不修改系统 PATH、用户 HOME 或全局软件安装。
- 缺少合适的本机 Node.js 不影响 App 启动、资讯、日历、模型榜和本地内容查看。
- 远端仓库、在线搜索和 Skill 安装需要网络；本地已有内容可以离线查看。私有 Git 仓继续使用你的 Git 凭据和 SSH key。
- **本次更新**：增加 AIHOT 资讯摘要、来源和相关 Skill 搜索；发现页展示近一周增长较快的 GitHub 项目、新发现项目及技能市场搜索；加入模型榜、重置日历，以及亮暗主题和更清晰的告警/冲突颜色。发现结果是候选信息，不是质量认证。
- 资讯、重置日历和模型榜会缓存到本机；联网使用时，以页面展示的更新时间判断缓存新旧。
- 右上角「同步本地」重新扫描本地技能，Git 仓库也可在来源区域更新。
- 配置与缓存保存在 `%USERPROFILE%\.skillmanage\`，不随压缩包分发。
- App 内含运行所需服务，双击即可打开，无需手动启动服务。联网浏览不要求安装 Node；执行 Skill 安装等 Node 操作要求本机 Node.js 22.20.0 或更新版本。缺少或过旧时，仅在相关操作中提示官方下载页。
- 从 v1.0.0 build63 升级时，旧 Windows 更新器只替换 exe，无法安装新版所需的 `runtime` 文件夹；请退出旧版、下载并完整解压 v1.1.0 build64 ZIP，保留完整目录。GitHub 旧版 v6.0.x 也不能自动识别此次跨版本号变化，请从 [GitHub Releases](https://github.com/YSBZ/skill-manager/releases/latest) 手动下载完整 ZIP 安装一次。之后使用新版应用内提示更新。

## 升级与卸载

手动升级时先退出 SkillManager，再用新版本压缩包中的整个文件树替换旧版本；运行时与程序版本需一起更新。

卸载时退出程序并删除解压目录；如不想保留配置，再删除 `%USERPROFILE%\.skillmanage\`。
