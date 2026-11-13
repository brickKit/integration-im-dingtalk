# integration-im-dingtalk

组件 `integration/im-dingtalk`（钉钉渠道成员）的仓库：把通知交给钉钉（互动卡片）、把钉钉里的按钮点击变成动作事件、让用户本人绑定钉钉账号。用法与边界见 `BRICKKIT.md`，为什么这样设计见 `docs/design.md`，代码地图见 `AGENTS.md`。旧版本（2.x）在分支 `legacy/v2`，标签保留；本主分支按终局设计从零开始（D59），版本从 `3.0.0` 起（A110）。

## 在项目里使用

在装配项目里 `brickkit add integration/im-dingtalk@<版本>`，按 `BRICKKIT.md` 的“部署前准备”配置钉钉应用与对外入口，再在通知组件的 `NOTIFICATION_CHANNELS` 里配上它。

## 文档

- `BRICKKIT.md`：定位、部署前准备、配置、契约索引。
- `docs/design.md`：术语、数据、规则 ID01–ID12、未决问题。
- `AGENTS.md`：代码地图、构建与测试。

## 开发

见 `AGENTS.md` 的“构建与测试”。
