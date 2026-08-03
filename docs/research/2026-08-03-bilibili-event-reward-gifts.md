# B 站直播活动/背包奖励礼物识别调研

调研时间：2026-08-03

## 结论

可以识别，但需要区分“实际送出后识别”和“提前发现”。

- 实际送出后可以可靠识别礼物本身：直播 `SEND_GIFT` 事件携带礼物 ID、名称、数量、价格、币种和礼物图片，本项目后台也会把首次出现的未知礼物写入最近礼物目录。
- 登录后可以读取“当前登录账号”的背包库存，用于提前发现该账号已经获得的活动奖励礼物；这不能代表所有观众的背包。
- `giftConfig` 的 `bag_gift=1` 只能说明礼物支持从背包赠送，不能证明它属于当前活动、仍可获得或当前用户持有。
- 不能把“不在直播间礼物面板”直接等同于“未上架”：它也可能是盲盒奖励、活动/背包礼物或仅在历史事件中出现过的礼物。

## 第一方接口实测

### 直播间礼物配置

B 站官方接口：

- `https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig`
- `https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftData`

对房间 `335790` 的 2026-08-03 响应进行交叉检查：`giftConfig` 返回 708 个礼物，其中 568 个带 `bag_gift=1`，但这些礼物没有直接出现在 `giftData` 的当前礼物面板列表中。列表包含明显的历史活动礼物，因此 `bag_gift` 不适合作为“当前活动中”的判定条件。

### 当前账号背包

B 站官方接口：

- `https://api.live.bilibili.com/xlive/web-room/v1/gift/bag_list`

匿名访问返回 `code=-101`、`message=账号未登录`。因此读取当前背包需要登录态；返回结果只描述登录账号自己的库存。

旧接口 `https://api.live.bilibili.com/gift/v2/gift/bag_list` 在匿名请求下返回空列表，不能用于判断真实库存。

## 本项目现状

- [`goserver/bilibili_source.go`](../../goserver/bilibili_source.go) 的 `parseBiliGift` 从 `SEND_GIFT` 读取 `giftId`、`giftName`、`num`、`price`、`coin_type`、`gift_info.img_basic` 和盲盒父礼物 ID。
- [`goserver/background_runtime.go`](../../goserver/background_runtime.go) 的 `applyGiftEvent` 会在执行规则前调用 `upsertRecentGiftState`，所以即使未知礼物没有命中任何规则，也会在实际收到后进入最近礼物目录。
- 当前礼物选择器把不在直播间面板中的最近礼物统一视作 `listed=false`；它可以通过搜索找到，但“未上架”标签无法区分活动背包礼物与真正的旧礼物。

## 推荐的数据分类

为每个礼物记录来源集合，而不是只存一个布尔值：

1. `panel`：当前直播间礼物面板可直接赠送。
2. `blind_box_reward`：当前已上架盲盒的奖励礼物。
3. `account_bag`：当前登录账号背包中实际持有。
4. `observed`：直播中实际收到过，但当前不在上述目录。
5. `catalog_only`：只存在于全量配置，未证明当前可用。

UI 建议显示为“面板可赠送”“盲盒奖励”“当前背包”“直播中收到过”“历史礼物”。默认展示前四类；搜索时再显示 `catalog_only`。

## 实现建议

1. 后端继续以 `SEND_GIFT` 作为最可靠的事后学习来源，不依赖活动接口名称。
2. 登录成功、房间连接成功和每隔数分钟刷新一次当前账号背包；按礼物 ID 合并，不需要高频轮询。
3. 保留 `bag_gift` 作为元数据提示，但不要用它直接标记“当前活动”。
4. 如果事件中存在稳定的背包来源字段，可以把它作为补充证据；在没有真实事件样本验证前，UI 应使用保守的“直播中收到过”，不要猜测为活动奖励。
5. 活动结束或背包库存变化时只移除 `account_bag` 来源，仍保留 `observed` 历史，避免已配置规则失效。
