# 2026-10-05 上游功能融合

本次在 CPA 的 Go/React 实现中融合五项已确认的改进。来源和验证分别登记于 [跟踪清单](../.github/upstream/codex-proxy-usage.json)，全仓跟踪起点、全仓审查状态及其他功能来源不变。

| 功能 | 上游来源 | CPA 行为 |
| --- | --- | --- |
| 明确统计时区 | [ab3a2e3](https://github.com/zyycn/codex-proxy-rs/commit/ab3a2e30c5941801bc553bab35bd2074047d3270) | 管理页传浏览器 IANA 时区，图表按本地日历分桶，与日期筛选及明细时间对齐。 |
| Fast 费用标识 | [d37dc32](https://github.com/zyycn/codex-proxy-rs/commit/d37dc3273147e5d472c4894578a7e0269445fbc9) | 费用旁显示 Fast，优先使用已保存计费档位，其次使用上游回报档位；仅请求了加速不作为已加速计费的证明。 |
| Codex 点数余额 | [0490e28](https://github.com/zyycn/codex-proxy-rs/commit/0490e28cfc14f08232a84c52e8c3f8693285b994) | 复用已有配额查询结果，单独显示余额，保留小数精度，区分零、无限、无可用点数及未提供余额。 |
| 弹窗退场内容 | [318f8a7](https://github.com/zyycn/codex-proxy-rs/commit/318f8a748e0ec389ce1e3ae6fca1c1d0819272f2) | 记录保留到关闭动画结束；修复快速 Escape 关闭时重新渲染取消退场的问题。 |
| GPT-6.1 Sol 离线价格 | [71c3b03](https://github.com/zyycn/codex-proxy-rs/commit/71c3b03878c36931eab71f4a7bd841202e7dfbdb) | 补充内置 Token 价格，保留动态价格优先及历史账单快照。 |

同时参考 [9c5fbdb](https://github.com/zyycn/codex-proxy-rs/commit/9c5fbdba70eed5575cb102922e29bf221e3c9c7d)，把 `frontend/src/components/usage/**` 纳入共享依赖跟踪，避免组件移动后漏报。

## API 与兼容性

`GET /v0/management/usage/dashboard` 和 `/usage/records` 接受可选 `timezone`。省略时仍为 UTC；空值、`Local` 或无效时区返回 400。Dashboard 返回实际 `timezone`，趋势时间戳仍是 UTC 表示的绝对时刻。二进制内嵌 IANA 数据以支持最小容器。

最近 24 小时、7 天、30 天仍是滚动区间；自定义日期仍使用浏览器时区，过滤保持半开区间。日桶按日历前进，可正确处理 23/25 小时日、午夜跳时；小时桶处理重复小时和半小时夏令时。旧后端无时区字段时，前端明确显示 UTC 图表，避免把旧桶标成本地时间。

账号点数余额与主动重置次数互不替代。刷新后上游缺少余额或请求失败时清除旧余额；不根据套餐、重置卡或账号池容量推测。浏览器配额验收使用合成响应，未调用真实账号上游接口。

## 价格核验

2026-10-05 核验 [OpenAI 模型文档](https://developers.openai.com/api/docs/models/gpt-6.1-sol) 与 [价格文档](https://developers.openai.com/api/docs/pricing)。每百万 Token 的标准价为：

| 上下文输入 | 输入 | 输出 | 缓存读取 | 缓存写入 |
| --- | ---: | ---: | ---: | ---: |
| 不超过 272,000 | $2 | $10 | $0.10 | $2.50 |
| 超过 272,000，整次请求适用 | $4 | $15 | $0.20 | $5 |

Priority/Fast 为标准价的 2 倍，Flex/Batch 为 0.5 倍。动态 LiteLLM 目录已经含该模型，本次只补离线内置回退，不覆盖动态目录、不重算历史账单。现有费用模型只统计 Token；工具调用费和地区附加价不在本次计量范围。

## 验证与交付

- 后端 Go 全量测试，usage/pricing/management race 检查，二进制构建及真实管理 API 冒烟通过。
- 时区回归覆盖纽约春秋切换、Kathmandu 非整小时偏移、São Paulo 午夜跳时和 Lord Howe 半小时夏令时；价格回归覆盖阈值、全部档位、动态目录覆盖与历史快照不变。
- 前端 `bun run verify`：572 项测试通过，TypeScript 和生产构建通过；保留一条已有 AccountPoolsPage lint 警告。
- 浏览器覆盖真实本地使用统计 API、刷新、旧后端 UTC、Fast、详情关闭/重开、精确余额及零/无限/缺失/失败状态、390px 暗色界面。
- 上游跟踪 16 项回归通过；两仓决策笔记校验通过。发布继续复用已有 Actions，前端完成后再将其版本固定到后端部署包。

这次没有迁入 Guardian 智能队列或完整插件架构，也没有改变账号池权限、独占租约、冷却、计费快照或调度策略。发布不包含线上部署。
