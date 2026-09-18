# codex-proxy-rs 用量功能适配与跟踪

## 来源与本次范围

- 来源项目：[zyycn/codex-proxy-rs](https://github.com/zyycn/codex-proxy-rs)。
- 本次审查的源码：[7f320797a06226d01ca77d1ff73980a843ee3efb](https://github.com/zyycn/codex-proxy-rs/tree/7f320797a06226d01ca77d1ff73980a843ee3efb)。审查时最新正式 Release 为 `v3.10.0`，该标签和本次源码提交不可混为同一版本。
- 许可：Apache-2.0。两个 CPA 仓库均保留原 MIT LICENSE，并分别附上 `NOTICE.codex-proxy-rs` 来源说明和从上述提交原样保存的 `LICENSE.codex-proxy-rs`。在此源码快照中未发现上游独立 NOTICE 文件；本地来源说明不是虚构的上游 NOTICE。
- 移植方式：参考行为、信息组织及数据接口，在 CPA 已有 Go 后端和 React 管理端实现；不引入 Rust 服务、Vue 运行时或上游 PostgreSQL 存储。

本次只适配使用统计相关能力：健康/性能/成本/趋势/维度诊断、服务端分页筛选、请求详情与 token/费用拆分、React 使用统计界面、脱敏诊断导出。账号调度、鉴权、租约、OAuth、代理传输和模型转换不在本次适配范围。

机器可读状态以 [codex-proxy-usage.json](../.github/upstream/codex-proxy-usage.json) 为准。最初登记为 `in_progress`；只有对应前后端实现及验证完成才改为 `adapted`。本说明不以参考源码已下载、代码已编写或构建已触发作为完成证明。

## 来源到本地模块的映射

| 功能 | 主要参考位置 | CPA 实现区域 |
| --- | --- | --- |
| 概览、健康、性能、成本、趋势、维度诊断 | `frontend/src/views/usage/`；`backend/crates/gateway-api/src/admin/observability/`；`backend/crates/gateway-store/src/postgres/observability/queries/usage.rs` | 后端 `internal/usage/`、`internal/api/handlers/management/`；前端 `src/features/usage/` |
| 分页及筛选 | `frontend/src/views/usage/composables/useUsageRecordsTable.ts`；`frontend/src/api/modules/usage.ts`；上游 observability query/use_case | 后端已有用量存储与管理 API；前端 `src/services/api/usage.ts` |
| 请求详情 | `UsageRecordDetailModal.vue`、`UsageDetailFieldGrid.vue`、`UsageTokenCell.vue`、`UsageBillingCell.vue`、`useUsageRecordDetail.ts` | 后端已有 RequestDetail、billing/token 契约；React 详情界面 |
| 管理界面组织 | `frontend/src/views/usage/index.vue` 及 components/composables/utils | `src/features/usage/` 与现有 CPA 国际化/组件 |
| 诊断导出 | `frontend/src/views/usage/utils/diagnosticsBundle.ts`、`RequestDiagnosticsPanel.vue` | 基于 CPA 实际可用字段构建明确白名单的脱敏导出 |

清单同时监控上游存储查询、领域模型、token/计费、接口测试、迁移、前端基础组件和依赖锁文件。依赖发生变化只是待审查信号，不意味着全部需要引入 CPA。

## 必须保留的 CPA 语义

1. `selected` 空分组无权限；显式 `all` 才包含全部启用组。管理展示和查询不能改变这个边界。
2. 保留既有账号租约、同组安全换号、到期/在途保护，以及 `previous_response_id` 的账号归属限制。
3. `selection_seq` 记录账号选择，不等于真实上游尝试次数。当前没有新建 attempt 采集；缺少的尝试、阶段耗时或上游返回字段显示不可用，禁止猜测补齐。
4. 请求结果、上游尝试与账号选择是不同统计口径；不得把一次最终成功掩盖的中间失败当成成功尝试，也不得把未知值表示为零。
5. 保留 CPA token accounting v2 的不重叠口径、计费质量和未定价覆盖率；不能把已含在输入/输出中的缓存或推理 token 再加一次。
6. 查询范围和导出应说明保留历史/明细上限；分页只在实际保留的数据中查询，不宣称拥有全量上游历史。
7. 导出不含 API key、OAuth token、原始身份头、请求/响应正文或密钥配置；使用允许字段列表，不直接序列化所有后端对象。

## 两种基线和单项状态

- `reviewed_commit`：已经按 `review_scope` 审查的上游提交，只覆盖登记的功能及依赖闭包；不表示全仓库已适配。
- `source_commit`：该功能本轮参考的候选上游提交。
- `adapted_commit`：该功能已经验证完成的上游来源 SHA；不是 CPA 本地提交号。未完成时为 `null`。
- `status`：`in_progress`、`adapted`、`deferred` 或 `rejected`。
- `local_areas` 和 `validation`：指向实际本地实现和验证证据。`adapted` 状态必须同时有完整来源 SHA 和非空验证记录。

不要因审查了新版本而统一推进所有功能的 `adapted_commit`。例如只适配分页修复，就只推进分页项；其他项维持原来源，写清暂缓或不适用理由。`reviewed_commit` 也只能在整组登记范围的差异都审查完后推进。

## 后续跟踪

[codex-proxy-usage-monitor.yml](../.github/workflows/codex-proxy-usage-monitor.yml) 每日 UTC 02:43（北京时间 10:43）及手动运行。它只读取上游默认分支、正式 Release 和提交差异，产出 Actions summary 与保留 30 天的 JSON/Markdown artifact；不写 Issue、不评论、不合并、不更新基线，也不部署。

检查分为两部分：当前 HEAD 对比 `reviewed_commit`，判断登记范围是否有未审查变化；每项 HEAD 对比其 `adapted_commit`，未适配时对比候选 `source_commit` 并明确标注“尚未适配”。上游正式 Release 改变仅记录信息，不自动提高本地适配状态。

GitHub 比较结果可能最多返回 300 个文件；达到这一界限、历史分叉或响应不完整时，报告要求完整人工复核。API 失败会使 workflow 失败，不能伪装成“没有变化”。重命名同时匹配旧路径，避免文件迁移漏报。

本地验证需要 Python 3 和已认证的 GitHub CLI（Actions 使用只读 `GITHUB_TOKEN`）：

```sh
python3 .github/scripts/test_codex_proxy_usage_monitor.py
python3 .github/scripts/codex-proxy-usage-monitor.py --output-dir /tmp/cpa-codex-usage-review
```

完整适配流程为：审查差异及依赖 → 判断保留/重实现/不适用 → 单项实现和回归 → 更新来源及验证记录 → 中文提交并推送 → 按已有工作流发布。重要行为变化仍须人工选择迁移方式。

## 发布与交付核对

后端 `auto-personal-release.yml` 在 `main` 验证 `go test ./...`、构建及管理 API 冒烟后创建个人标签，再调度 `release.yaml`。正式发布要求标签属于 main 历史，所有 10 个平台包完成后发布最终 checksums 并取消 draft。前端 `auto-cpa-release.yml` 在验证后创建 CPA 标签，`release.yml` 再执行 `bun run verify` 生成 `management.html`。

后端每个平台包包含两个来源/许可证文件；前端 Release 单独附带这两个文件和原 MIT LICENSE。配套部署包还应包含源码补丁、这次管理页面、部署说明、构建参数、来源记录及上述许可证，并附 SHA-256。不得加入真实配置、认证文件或密钥。下载发布附件后核对 SHA-256 与归档内容；CI 成功、公开发布和生产部署分别报告。本次发布不自动更新线上服务。
