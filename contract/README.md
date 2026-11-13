# integration/im-dingtalk 的契约

- `openapi.yaml`：用户面（回调、绑定、隔离区）。
- `events/`：发布的两种事件的 schema，是线协议的共享 schema（`schemas/channel/action.v1`、`delivery.v1`）的副本。
- 系统面是线协议的共享契约 `be.channel.v1.ChannelService`（`proto/be/channel/v1/channel.proto`，SDK 生成为 `channelpb`），本组件没有自己的 proto，契约不单独发布标签。
