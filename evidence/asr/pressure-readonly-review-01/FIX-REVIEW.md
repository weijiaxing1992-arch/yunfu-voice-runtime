# 私有压测诊断补丁的只读复核

复核位置：`work/voice-runtime-m2-asr/benchmark-fix-01/control`。不修改该副本或 project，不启动测试/网络。此记录对应邀请审查时的采样逻辑，后续修改需另行验证。

**需要收紧的一项声明：** `benchmarkWorkerStatsAfterExit` 把 `Admission.SampledAt > generatorExitedAt` 作为全部媒体统计在退出后的证据。但当前 `control/internal/media/pool.go:774–783` 在同步 stats RPC 返回之后，才以 `time.Now()` 发布 Admission.SampledAt；因此一个在退出前生成的统计可以在退出后才被接收并获新时间戳。新补丁中的时间条件只能证明控制器在退出后发布过统计更新，不能严格证明 Rust 生成计数的时刻越过退出。

此外 `control/internal/server/server.go:572` 先读取 `worker.Snapshot()`，再读取 `worker.Admission()`；pool 分别发布统计和 Admission，两次读取可跨更新，得到旧统计与新时间戳。它不一定造成此次历史失败，但构成真实可达的观测错配，不能用墙钟条件消除。

本轮可先准确标注“控制器在退出后收到更新；Rust 计数生成边界未独立证明”，保留统计参考价值且仍保持 failed。若要给出严格终态统计保证，后续应把计数、同一 RPC 的请求起点与返回时间作为同一不可变对象原子发布；只有请求起点晚于退出且身份一致时，才证明该 RPC 的生成顺序。此项不得通过单纯放宽 freshness 阈值修复。

其余本次已读逻辑中，`observeFailureFinal` 的最后成功样本与其 `latestAt` 一起保留，后续读取失败单列 error；取消/超时没有把样本时间换成失败请求的时间。零 socket 丢弃计数只有全分片明确支持时显示数字，否则显示未提供；非零已记录值保留并注明完整计数未提供，未发现新增虚假零值问题。根任务另在补 `runReal` defer 的原失败策略测试，尚不在本记录的验证范围。
