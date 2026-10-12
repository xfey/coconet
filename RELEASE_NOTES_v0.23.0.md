# Coconet 0.23.0

Devices can now take over earlier failed or expired background DAG tasks before processing their own new work. The model request and evidence mapping are fixed before the first attempt. The helper uses its own Agent and default model while preserving source authorship; task claims, renewal and atomic result acceptance fence out expired workers. Independent conversations remain concurrent.

Inputs are temporary: 1 MiB per task, 32 MiB per project, 256 MiB globally and a 24-hour lifetime. Completion and supersession remove the input immediately. Each device can automatically attempt a task twice, with three shared attempts in total; explicit retry resets the allowance. The Relay does not run models. Full outputs and diagnostics remain on the executing device. Unsynced or unregistered updates cannot be taken over. Explicit historical imports retain their local approved budgets.

Dependency input now identifies earlier work already included in the current stage. Revising that same stage does not require a separate parent; distinct earlier collaboration results still require evidence. This release also includes the TCodex app-server startup-output compatibility fix and the recent minimal Dashboard copy and refresh-state changes.

设备开始自己的新任务前，可以接管此前失败或领取超时的后台构图任务。接管使用相同的固定请求与证据映射，使用接手设备的 Agent 默认模型，保留来源作者。服务器校验领取及结果提交，过期执行者无法重复写入；不同会话仍能并发处理。

任务输入单份最多 1 MiB、单项目 32 MiB、全局 256 MiB，最多保留 24 小时，完成或被替代即删除。单设备最多自动尝试两次，总共三次；全部失败后显式重试。服务器不调用模型，完整输出与日志仍在执行设备。未同步或未注册的更新不能接管；显式历史导入不跨账号转移预算。

关系输入区分当前节点自身的已有成果与外部依赖，避免同阶段返修被误判为缺少候选。版本同时包含 TCodex app-server 启动输出兼容修复及近期 Dashboard 文案、刷新状态更新。

Upgrade the Server first, then all participating clients to 0.23.0. Earlier clients cannot read the new processing commits. Accounts, projects and folder connections are preserved. Client / Server / npm / both Plugins use 0.23.0. This public repository distributes documentation and artifacts only.

先升级服务器，再将所有参与协作的客户端升级到 0.23.0。旧客户端不能读取新格式的处理提交；现有账号、项目与目录关联保留。公共仓库只分发文档和制品。

Validated with isolated interactive TClaude / TCodex sessions on macOS and Linux: bidirectional handoff preserved the exact fixed input and source author, both replicas converged to two nodes and one dependency, and offline Search / fixed-source Pull passed. Full engineering checks and targeted race tests passed. These samples do not guarantee identical model output or success on every attempt.

本机与 Linux 开发机的隔离 TClaude / TCodex 交互验收通过：双向接管保留相同固定输入与来源作者，两端最终收敛为两个节点、一条依赖边，离线搜索与固定来源读取正常。完整工程检查和定向竞态测试通过；模型输出仍需校验，不保证每次成功。
