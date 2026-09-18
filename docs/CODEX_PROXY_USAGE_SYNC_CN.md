# codex-proxy-rs 全项目更新跟踪与用量功能适配

## 来源与本次范围

- 来源项目：[zyycn/codex-proxy-rs](https://github.com/zyycn/codex-proxy-rs)。
- 本次审查的源码：[7f320797a06226d01ca77d1ff73980a843ee3efb](https://github.com/zyycn/codex-proxy-rs/tree/7f320797a06226d01ca77d1ff73980a843ee3efb)。审查时最新正式 Release 为 `v3.10.0`，该标签和本次源码提交不可混为同一版本。
- 许可：Apache-2.0。两个 CPA 仓库均保留原 MIT LICENSE，并分别附上 `NOTICE.codex-proxy-rs` 来源说明和从上述提交原样保存的 `LICENSE.codex-proxy-rs`。在此源码快照中未发现上游独立 NOTICE 文件；本地来源说明不是虚构的上游 NOTICE。
- 移植方式：参考行为、信息组织及数据接口，在 CPA 已有 Go 后端和 React 管理端实现；不引入 Rust 服务、Vue 运行时或上游 PostgreSQL 存储。

本次只适配使用统计相关能力：健康/性能/成本/趋势/维度诊断、服务端分页筛选、请求详情与 token/费用拆分、React 使用统计界面、脱敏诊断导出。账号调度、鉴权、租约、OAuth、代理传输和模型转换不在本次适配范围。

后续更新跟踪覆盖整个上游项目，包含源码、功能、修复、依赖、文档、CI 与正式 Release。已实现的五项用量功能继续单独记录适配来源；整个项目纳入跟踪，不代表其他模块已经审查或迁入 CPA。

机器可读状态以 [codex-proxy-usage.json](../.github/upstream/codex-proxy-usage.json) 为准。最初登记为 `in_progress`；只有对应前后端实现及验证完成才改为 `adapted`。本说明不以参考源码已下载、代码已编写或构建已触发作为完成证明。

## 来源到本地模块的映射

| 功能 | 主要参考位置 | CPA 实现区域 |
| --- | --- | --- |
| 概览、健康、性能、成本、趋势、维度诊断 | `frontend/src/views/usage/`；`backend/crates/gateway-api/src/admin/observability/`；`backend/crates/gateway-store/src/postgres/observability/queries/usage.rs` | 后端 `internal/usage/`、`internal/api/handlers/management/`；前端 `src/features/usage/` |
| 分页及筛选 | `frontend/src/views/usage/composables/useUsageRecordsTable.ts`；`frontend/src/api/modules/usage.ts`；上游 observability query/use_case | 后端已有用量存储与管理 API；前端 `src/services/api/usage.ts` |
| 请求详情 | `UsageRecordDetailModal.vue`、`UsageDetailFieldGrid.vue`、`UsageTokenCell.vue`、`UsageBillingCell.vue`、`useUsageRecordDetail.ts` | 后端已有 RequestDetail、billing/token 契约；React 详情界面 |
| 管理界面组织 | `frontend/src/views/usage/index.vue` 及 components/composables/utils | `src/features/usage/` 与现有 CPA 国际化/组件 |
| 诊断导出 | `frontend/src/views/usage/utils/diagnosticsBundle.ts`、`RequestDiagnosticsPanel.vue` | 基于 CPA 实际可用字段构建明确白名单的脱敏导出 |

这五项功能的来源筛选同时覆盖上游存储查询、领域模型、token/计费、接口测试、迁移、前端基础组件和依赖锁文件。这些路径只用于判断现有适配项可能受哪些变化影响，不限制全仓更新报告。依赖发生变化只是待审查信号，不意味着全部需要引入 CPA。

## 必须保留的 CPA 语义

1. `selected` 空分组无权限；显式 `all` 才包含全部启用组。管理展示和查询不能改变这个边界。
2. 保留既有账号租约、同组安全换号、到期/在途保护，以及 `previous_response_id` 的账号归属限制。
3. `selection_seq` 记录账号选择，不等于真实上游尝试次数。当前没有新建 attempt 采集；缺少的尝试、阶段耗时或上游返回字段显示不可用，禁止猜测补齐。
4. 请求结果、上游尝试与账号选择是不同统计口径；不得把一次最终成功掩盖的中间失败当成成功尝试，也不得把未知值表示为零。
5. 保留 CPA token accounting v2 的不重叠口径、计费质量和未定价覆盖率；不能把已含在输入/输出中的缓存或推理 token 再加一次。
6. 查询范围和导出应说明保留历史/明细上限；分页只在实际保留的数据中查询，不宣称拥有全量上游历史。
7. 导出不含 API key、OAuth token、原始身份头、请求/响应正文或密钥配置；使用允许字段列表，不直接序列化所有后端对象。

## 全仓跟踪基线、功能审查与单项适配状态

- `monitor_scope`：固定为 `entire_repository`，所有上游路径均纳入全仓更新检查。
- `repository_tracking_commit`、`repository_tracking_release`：全仓跟踪起点，首次启用沿用已知源码 `7f320797…` 和正式 Release `v3.10.0`。该提交当时只审查了用量范围，不能据此声称全仓已经审查。
- `repository_reviewed_commit`、`repository_reviewed_release`：全项目变化完成审查后才登记的基线，初始为 `null`。全仓差异优先对比此提交；未登记时对比跟踪起点。
- `reviewed_commit`：原有功能审查基线，仍只表示已经按 `review_scope` 审查五项登记功能及依赖闭包；既不代表全仓审查，也不代表全仓适配。`review_notes` 继续限定在这些功能的审查范围。
- `source_commit`：该功能本轮参考的候选上游提交。
- `adapted_commit`：该功能已经验证完成的上游来源 SHA；不是 CPA 本地提交号。未完成时为 `null`。
- `status`：`in_progress`、`adapted`、`deferred` 或 `rejected`。
- `local_areas` 和 `validation`：指向实际本地实现和验证证据。`adapted` 状态必须同时有完整来源 SHA 和非空验证记录。

不要因审查了新版本而统一推进所有功能的 `adapted_commit`。例如只适配分页修复，就只推进分页项；其他项维持原来源，写清暂缓或不适用理由。`reviewed_commit` 也只能在整组登记范围的差异都审查完后推进。

## 后续跟踪

[codex-proxy-usage-monitor.yml](../.github/workflows/codex-proxy-usage-monitor.yml) 的工作流名称为 `codex-proxy-project-monitor`，每周一 UTC 02:43（北京时间 10:43）及手动运行。它只读取上游默认分支、正式 Release 和全仓提交差异，产出 Actions summary 与保留 30 天的 JSON/Markdown artifact；不写 Issue、不评论、不合并、不更新基线，也不部署。沿用已有文件名以兼容链接和调用。

检查分为三层：报告的 `review` 对比全仓基线与当前 HEAD，不按用量路径过滤；`feature_review` 对比原有 `reviewed_commit`，只判断登记功能范围；`features` 则逐项对比其 `adapted_commit`，未适配时对比候选 `source_commit` 并明确标注“尚未适配”。全仓有变化不会自动把五项功能都标记为需要移植。

正式 Release 与全仓 Release 基线单独比较，即使 HEAD 和净文件差异没有变化，新 Release 仍令顶层 `review_required` 为真，并在 Markdown 报告中明确展示前后版本。Release 推进不会自动提高任何功能的适配状态。净文件差异为空但存在新提交（例如变更后又回退）时，也保留全仓审查信号。

配套定期 AI 检查安排在每周一北京时间 10:50，结合上述报告评估整个项目更新是否适合迁移。评估包括新能力、修复和依赖闭包，保留 CPA 已有语义；不自动合并或部署。无需跟踪所有 Issue、PR 评论。

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
