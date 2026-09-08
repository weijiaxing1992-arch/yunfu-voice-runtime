# ASR1 协议与真实链路验收矩阵

本矩阵不改变现有 FreeSWITCH 对照状态。`offline_model` 仅指本目录自测；`real_not_run` 表示下一批必须实际执行，不能根据模拟对象调用或计划文本改绿。实际离线通过数、失败原件和指纹见新 `selftest-*/receipt.json`。

## 协议与模型验收

| ID | 检查与错误注入 | 本目录对应方法（省略 test_ 前缀） | 下一批真实通过条件 |
|---|---|---|---|
| ASR-P01 | 固定 112 字节头、8k 非零样本、16k 格式 | fixed_header_offsets_and_nonzero_pcm；16k_is_320_samples_with_8k_rtp_clock | 原始 wire 独立解析全部头与完整 PCM；16k 另外验 FIR |
| ASR-P02 | 全部分片位置、多帧合读 | all_single_split_positions_preserve_bytes；fragmented_and_multiple_messages | 独立 Unix 写分片不丢/不拼错；总帧期限不续期 |
| ASR-P03 | 超长/过短 length、非法类型/版本/保留位 | oversize_and_short_length_reject_before_body；unknown_prefix_values_rejected | 读正文前拒绝，实际峰值读缓冲有界 |
| ASR-P04 | JSON 重复键、非法 UTF-8、NaN、尾随、u64 溢出 | json_duplicate_unicode_nonfinite_and_trailing；u64_canonical_and_overflow | 双方 Go/独立模拟进程按同合同拒绝 |
| ASR-P05 | HELLO/READY 严格能力及顺序 | hello_required_and_capabilities_exact | 未 READY 时不订阅；错误 rate/token/时钟不容错；握手总 2s |
| ASR-P06 | 错 token/缺来源边界/未声明位置 | audio_requires_matching_token_and_source_boundary；summary_cannot_claim_audio_position_or_deadline | 不能跨呼叫/订阅/worker 接收旧音频 |
| ASR-P07 | PLC origin/错误长度/保留位 | audio_plc_origin_length_and_reserved_flags_reject | 仅 Decoded 上传；不得通过改 origin 冒充 PCM 来源 |
| ASR-P08 | 上传中 partial 与 final 摘要 | partial_during_input_and_final_digest_actual_pcm | FINISH 前收到 partial；独立 PCM 重算摘要及逐帧计数 |
| ASR-P09 | PLC/CN/缺包/辅助事件不当沉默 | plc_cn_missing_and_dtmf_never_add_samples；only_cn_has_sid_and_auxiliary_duration_is_unknown | 真实 RX 类型逐项匹配；SID 原样；無补零及假 endpoint |
| ASR-P10 | ExportGap 保真 | gap_range_is_observation_count_not_silence_duration | first/last/decoded/plc/reasons 与原始 RXS2 对账，不推断时长 |
| ASR-P11 | 源边界/倒序/事件缺口 | source_boundary_resets_hash_and_utterance；reversal_and_sequence_hole_are_rejected | 新来源清 FIR 历史，完整展开位置不回退；无边界不接续 |
| ASR-P12 | 零真实输入/失败类型 | zero_decoded_returns_no_invented_final；observation_failure_is_terminal_not_final | CN/PLC 不能增加识别成功音频；失败不得伪造完成 |
| ASR-P13 | expires 精确边界、错误域/未来 lower | exact_expiry_future_origin_and_wrong_domain | 真实共享时钟核对；同机同 namespace；过期拒绝 |
| ASR-P14 | 写前检查/部分写不重锚 | deadline_is_immutable_on_retry；partial_submission_expires_then_cannot_resume | 保存每次实际写前时钟、原期限、提交字节；未知关闭且无重放 |
| ASR-P15 | 完整接收后暂停及 marker 期限 | provider_rechecks_after_full_read_preemption；ordinary_marker_also_expires_but_summary_has_no_age | 模拟进程实际读后暂停，恢复不得消费旧帧；summary 无伪期限 |
| ASR-P16 | 握手/完整帧/Finish 总期限 | total_handshake_and_finish_deadline_not_renewed_by_progress | 实际慢速分片不能延长总期限；Cancel 唤醒阻塞 IO |
| ASR-P17 | 起建/未知清理占位 | starting_and_unknown_cleanup_retain_quota | 超额立即拒绝，必须确认 RX 和连接关闭才还额度 |
| ASR-P18 | FINISH 输入前缀 | finish_prefix_must_be_exact | 以完整实际提交前缀计数，不把产生/写尝试视为提交 |
| ASR-P19 | End、final、EOF 与完成区别 | input_end_alone_is_not_success；normal_finish_final_done_and_rx_stop_are_distinct；final_or_eof_without_done_cannot_complete | 有主动 Finish 意图 + DONE + RX 实停 + 结果交付；其他 EOF 失败 |
| ASR-P20 | DONE 越序/样本前缀错误 | done_before_finish_wrong_prefix_and_unfinalized_partial | DONE 对齐真实 provider 已消费计数，不能伪造确认 |
| ASR-P21 | 挂断/迟到旧来源结果 | hangup_drops_pending_and_late_transcripts；source_boundary_invalidates_partial_and_discards_old_result | 真实 BYE/替换/worker 失效之后最后交付检查拒绝；计 stale |
| ASR-P22 | token/范围/结果顺序/重复 final | wrong_identity_range_order_and_duplicate_final_fail；partial_revision_and_single_open_utterance | 单开放 utterance；final 不改写；范围必须属实际提交来源 |
| ASR-P23 | 慢业务消费/partial 洪泛/final 溢出 | partial_coalescing_stays_bounded；final_overflow_is_explicit_failure | 实际 queue≤8；无媒体阻塞；错误独立终态槽可查 |
| ASR-P24 | ERROR/半帧 EOF | provider_error_stays_failure；half_frame_eof_is_error | 明确失败，无 DONE 不归 completed；连接和 RX 分别回收 |
| ASR-P25 | 18 个计划自身有限 | fault_manifest_is_finite_and_does_not_execute_delays | 计划由实际独立 mock 实施；计划存在不计网络用例通过 |
| ASR-P26 | CN/PLC/gap 后时间仍不可倒退 | marker_does_not_reset_same_source_time_lower_bound；complete_rtp_numbers_cannot_go_back_after_marker | 连续性清理不能清同源下界，完整 u64 不只看低位 |
| ASR-P27 | InactiveSuspended 撤销开放 partial | inactive_suspends_utterance_until_new_boundary；suspension_invalidates_partial_and_blocks_old_source_until_boundary | 真实恢复先有新来源边界，旧结果不跨暂停接续 |
| ASR-P28 | lower/expiry 同时移动 | shifting_both_deadlines_cannot_hide_expired_source | 原 RX → 转换 → 待发逐字段绑定；不因配对仍相差 100ms 放行 |
| ASR-P29 | ERROR null/空值复活 | malformed_error_cannot_revive_consumer | 无效错误消息也锁定失败，之后 final/DONE 不得完成 |
| ASR-P30 | 仅 marker/新来源无音频/虚假范围 | results_require_actual_decoded_not_only_markers；result_cannot_claim_missing_events_or_complete_across_gap；result_media_range_must_match_actual_input | 端点和媒体区间必须对应已完整提交 Decoded；不能从事件号上界推断音频 |
| ASR-P31 | 来源证明内存与裁剪 | submitted_provenance_is_bounded_and_final_releases_ranges | 元数据区间有限；final/来源清理释放，超额明确失败而非失去证明后继续 |
| ASR-P32 | 辅助事件序号不是音频缺口 | auxiliary_event_number_does_not_invent_audio_gap | Provider→Consumer 实际往返，DTMF marker 前后连续音频可 complete，不强制 discontinuous |

## 下一批必须执行的真实用例（全部 real_not_run）

| ID | 真实环境与输入 | 故障计划/动作 | 必须独立检查的结果 |
|---|---|---|---|
| ASR-R01 | PCMU × 普通 UDP × 8k | 非静音 RTP，错误 ACK→正确 ACK，partial_stream | 错 ACK 不授权；正确 ACK 后实际 ASR1 8k 逐样本等于独立 G.711 oracle |
| ASR-R02 | PCMA × 普通 UDP × 8k | 同上 | 独立 A-law 样本、来源与完整 RTP 展开回绕 |
| ASR-R03 | PCMU × connected UDP × 8k | 同上 | connected socket 不影响上行接收/来源隔离 |
| ASR-R04 | PCMA × connected UDP × 8k | 同上 | 同上；四组合各自新目录、全样本留存 |
| ASR-R05 | 上述 4 组合 × 16k | 连续非零输入、脉冲、正弦、切帧重排、源变更/缺口 | 真正 127 抽头 FIR：320/帧、连续/分帧等价、饱和、通带/镜像抑制、3,937,500ns 延迟；不以 Python 构造的 16k 输入当输出 oracle |
| ASR-R06 | 实际丢 RTP、乱序、PLC、CN SID、telephone-event | 精确原始事件检查 | ASR MARKER 与 RXS2 同步；PLC/CN/辅助事件不给 Decoded 计数、不造零 PCM |
| ASR-R07 | 实际上行队列拥塞和 SSRC/序号/时间源边界 | 原始 gap + late_old_segment | 缺口逐记录对账；源转换清 FIR；旧 partial 失效；完整展开位置不回退 |
| ASR-R08 | 取 RX 后、转换后、写权限后分别暂停 | sdk_expired / resample_expired / write_lock_expired | 每检查点原期限不变；发送为零或明确未知；不得追赶发送旧样本 |
| ASR-R09 | 写入 17 字节后实际暂停 | partial_write_expired | 保存真实返回字节和时钟；submission_unknown；连接关闭；无下一消息和重放 |
| ASR-R10 | provider 完整收 AUDIO 后暂停 | provider_consumption_expired | 独立进程确实持有完整原帧但未消费；恢复后拒绝；消费样本不增 |
| ASR-R11 | 未 READY、读正文慢、PONG 丢失、Finish 不回 DONE | ready_timeout / 慢速小分片 / 丢 PONG | 总期限生效；未订阅不造占用；超时清理有界，启动/未知占位可见 |
| ASR-R12 | 一流暂停，其他流持续；至少 65 个起建 | slow_reader + 第 65 流过载 | 默认 64 额度含 starting/cleanup_pending；其他通话/DTMF/TX 有进展；实际协程/RSS/FD 有界 |
| ASR-R13 | Finish 后通话保持活跃 150ms 再返结果 | late_final_after_finish | 合法 late final/DONE 可以交付；主动退订不等于 LifetimeDone；RX 实际停止前不报 completed |
| ASR-R14 | 与 R13 同窗口实际 SIP BYE | late_final_after_hangup | BYE 200 后不发布未读/迟到结果；正确计 stale；不可使用 ctx.cancel 冒充真实挂断 |
| ASR-R15 | Finish 窗口 worker 死亡/订阅替换/服务停止 | 分别注入真实生命周期变化 | 所有旧句柄最终授权撤销；迟到消息不能进入新 token/订阅 |
| ASR-R16 | 错误结果与连接错误 | wrong_token / duplicate_final / wrong_done_prefix / close_before_done / half_frame_eof / oversize_length | 全部失败但 SIP/TX 不被当作 ASR 正常结束；原始错误帧保留 |
| ASR-R17 | 慢 Next、partial 洪泛、9 个未读 final | result_overflow | partial 合并计数可重算；final 溢出明确失败；内存保持上限 |
| ASR-R18 | 与 RX 同通话并行 RVA1 非静音 TX | 两边不同独立样本序列，停止/失败 ASR | provider 只收 A/RX；下行样本不混入；ASR 错误不隐式 INTERRUPT |
| ASR-R19 | 上述正常与失败结束后的真实回收 | 正常 Finish/BYE 与失败清理分别记录 | RX 停止/退休、ASR active/starting/cleanup_pending、真实 call/media active、32s SIP 定时器均归零；四媒体端口可重绑；强杀不当正常退出 |

## 证据合同与计数

每次新建运行目录，至少保存：

1. 候选 Go/Rust/模拟供应商/测试二进制 SHA，全部相关源码/配置/依赖前后 SHA；包含新增本地 Go/C/汇编输入及 vendor，不能仅绑定已有白名单。
2. 原始 SIP 请求/应答/实际对端、错 ACK 与正确 ACK 的完整头；原始入站 RTP、RXS2、ASR1 双方向字节及完整 PCM；结果原文与实际到达/交付时钟。
3. 原始来源与时效：完整 u64 包序/时间、generation/segment、原 lower/expiry、实际写前/消费前共享时钟、暂停检查点及已经提交的字节范围。
4. produced、SDK-delivered、adapter-written、provider-consumed/acknowledged 分别计数；这些是四个阶段，不可用一个“识别样本”总计替换。MARKER、gap、expired、unknown_write、stale_result 和结果合并/溢出单列。
5. 实际进程命令/退出或强清原因、RX 状态、额度/FD/协程/RSS、高峰与回收、32s SIP 定时器以及四端口重绑。未知退出码留 unknown，不能补造 0。

证据分析器必须从原始字节和样本重算，不信任 fixture 的 passed 标记；失败原件保留，新候选使用新目录。不得放宽 deadline、跳过失败组合、丢失样本或将模拟摘要的文本质量描述成真实 ASR 识别质量。

完成全部 ASR-R 用例仅可关闭“私有中立协议模拟供应商链路”子项。真实云厂商鉴权/TLS/WebSocket/gRPC、识别效果、VAD/TTS/LLM、Agent 自动打断、录音及万路性能均另行验收，不能借本矩阵改为已完成。
