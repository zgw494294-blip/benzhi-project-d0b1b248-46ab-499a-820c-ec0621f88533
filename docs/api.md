# HTTP API

基础路径是 `/api/v1`，请求和响应使用 JSON。写请求必须提供 `X-Actor`。统一错误字段为 `code`、`message`、`details`、`request_id`；校验错误返回 422，版本或状态冲突返回 409，不存在返回 404，请求体超过 1 MiB 返回 413。

## 路由

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/healthz` | 仓储、审计链和任务健康状态 |
| POST | `/rule-sets` | 创建规则草稿 |
| PUT | `/rule-sets/{id}` | 按版本替换规则草稿 |
| POST | `/rule-sets/{id}/publish` | 发布规则 |
| GET | `/rule-sets/{id}` | 读取规则 |
| POST | `/qualifications` | 创建工艺评定 |
| GET | `/qualifications` | 筛选评定列表 |
| GET | `/qualifications/{id}` | 读取评定与证据 |
| POST | `/qualifications/{id}/evidence` | 幂等添加证据 |
| POST | `/qualifications/{id}/evaluate` | 评定证据完整性 |
| POST | `/qualifications/{id}/withdraw` | 撤回合格评定 |
| POST | `/procedures` | 创建规程首版 |
| POST | `/procedures/{id}/revisions` | 创建后续修订 |
| POST | `/procedure-revisions/{id}/derive-coverage` | 推导共同覆盖范围 |
| GET | `/procedure-revisions/{id}/coverage` | 读取范围与缺口 |
| POST | `/procedure-revisions/{id}/publish` | 发布规程修订 |
| POST | `/joint-requirements` | 幂等登记生产接头 |
| GET | `/joint-requirements/{id}` | 读取接头要求 |
| POST | `/joint-requirements/{id}/assessments` | 生成不可变评估 |
| GET | `/assessments/{id}` | 读取结论和全部差异 |
| POST | `/change-reviews` | 创建修订变更审查 |
| POST | `/change-reviews/{id}/decide` | 签发审查决定 |
| GET | `/review-tasks` | 分页查询复核任务 |
| GET | `/cases/{type}/{id}/export` | 导出带摘要的审查包 |

列表接口使用 `limit` 和不透明 `cursor`，`limit` 范围为 1 至 200。服务严格拒绝未知 JSON 字段、重复字段和尾随内容。

