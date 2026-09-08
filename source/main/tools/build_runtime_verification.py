#!/usr/bin/env python3
"""真实运行证据与当前源码绑定；独立于FreeSWITCH实现状态和静态验证账本。"""
import argparse
import base64
from collections import Counter
from datetime import datetime, timedelta, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import uuid
import zlib
import pcm_verification
import rx_verification

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_EVIDENCE = ROOT.parents[1] / 'work/audio-task/runtime-verification'
HISTORY = ROOT.parents[1] / 'work/audio-task'
LIFETIME_DAYS = 30
GO_COMMAND = ['go', 'test', '-race', '-count=1', '-json', '-timeout=3m', './...']
# Go包的本地汇编/CGO输入也参与构建；新增此类文件必须撤销旧证据。
GO_NATIVE_SUFFIXES = ('s', 'S', 'c', 'h', 'cc', 'cpp', 'cxx', 'm', 'mm', 'f', 'F', 'f90', 'for', 'syso', 'swig', 'swigcxx')
STATES = {'passed', 'failed', 'not_run', 'stale', 'missing_evidence', 'unbound'}
LABELS = {'passed': '本地验证通过（限定范围）', 'failed': '本地验证失败', 'not_run': '本地验证未执行',
          'stale': '证据已过期或源码已变化', 'missing_evidence': '证据缺失或校验失败', 'unbound': '历史运行未绑定当前源码'}
# M1 是独立执行器的窄范围证据，不能冒充旧二十四阶段，也不能授予原版 jitterbuffer API 绿色。
PROCESSED_KIND = 'python_unittest_processed_media_e2e'
PROCESSED_KEY = 'key-media-transcoding'
PROCESSED_SCOPE = ('两种 UDP socket 模式各完整 11 项真实双腿 G.711 PCMU/PCMA、8kHz、20ms 转码；'
                   '40ms 缓冲/120ms 上限、重排/迟到/CN/DTMF、基本 RTCP 和回收。'
                   '仅本地限定功能，不包含 G.722/Opus、其他帧长、本地 IVR、ASR/TTS、FreeSWITCH 原版 API 或并发容量认证。')
PROCESSED_TESTS = tuple('ProcessedMediaIntegration.test_' + suffix for suffix in (
    '183_without_aux_then_200_restores_dtmf_without_restarting_media',
    'bidirectional_pcmu_pcma_decodes_changes_and_reencodes',
    'bounded_reorder_and_duplicate_do_not_duplicate_audio',
    'bye_reclaims_graph_and_stale_source_cannot_feed_reused_ports',
    'cn_then_marker_with_new_timestamp_phase_resumes_real_audio',
    'dynamic_a_pcma_and_static_b_pcmu_keep_independent_sdp',
    'interleaved_dtmf_and_cn_use_new_timeline_without_false_audio_loss',
    'missing_frame_then_late_arrival_is_not_replayed',
    'non_g711_and_non_20ms_offers_are_rejected_before_upstream',
    'rtcp_reports_are_terminated_count_media_and_finish_with_bye',
    'worker_pause_expires_old_audio_without_catchup_burst'))
PROCESSED_REJECT = PROCESSED_TESTS[8]
SERVER = 'rustswitch/control/internal/server'
SIP = 'rustswitch/control/internal/sip'
MEDIA = 'rustswitch/control/internal/media'
# 人工审查的断言范围，不把单个测试扩散为原版接口所有分支或全部功能已通过。
CLAIMS = {
 'key-sip-register': (SERVER, ['TestRegistrationRejectsForeignResponsesAndLoops','TestRegistration423ExpiryTimeoutAndShutdown','TestRegistrationStaleChallengeBound','TestInviteDigestRetriesPreserveACKAndCancellation','TestInviteAuthenticationInvalidChallengesFailClosed','TestTrunkRegistrationAndInviteRealRustMedia/udp','TestTrunkRegistrationAndInviteRealRustMedia/tcp','TestTrunkRegistrationAndInviteRealRustMedia/tls'], '真实UDP/TCP/TLS注册、64包双向Rust媒体、ACK/BYE认证和注销；固定上游REGISTER/Digest客户端、认证重试、错误来源/循环/期限与取消状态机；不代表终端注册服务器或运营商认证'),
 'key-esl-sendmsg': (SERVER,['TestApplicationSetUnsetHasActualStateAndOrderedEvents','TestApplicationReadSerializesQueuedCommandsAndLegDigits','TestApplicationQueueCancellationAndHeapAreBounded'],'受限应用串行执行、变量副作用和挂断取消/资源预算；并非原版全部APP'),
 'key-esl-framing': ('rustswitch/control/internal/esl', ['TestFrameEverySplit','TestResponseBinaryBody','TestFrameRejectsAmbiguousLength'], '有界字节分帧、中文正文、粘包及歧义长度拒绝；完整原版差分另见成对报告'),
 'key-esl-api': (SERVER, ['TestCompatibilityChannelIdentityAndVariables','TestCompatibilityVariableBoundsAndCancelledMutation'], '真实两腿UUID与普通变量边界；只覆盖当前有限API，不含全部原版命令'),
 'key-esl-bgapi': ('rustswitch/control/internal/esl', ['TestBackgroundJobCorrelation','TestQueueSaturationAndShutdown'], '后台作业关联、真实正文、固定并行度及取消；完整Job字段和崩溃恢复不在范围内'),

 'key-http-authentication': (SERVER, ['TestAdminProtection'], '自有管理接口Host、CSRF及Content-Type拒绝规则；不包含远程用户认证'),
 'key-http-config-read': (SERVER, ['TestAdminConfigurationWaitsForRestart', 'TestAdminConfigSnapshotOwnsSlices'], '活动/待重启配置分离及配置快照切片所有权'),
 'key-http-config-write': (SERVER, ['TestAdminConfigurationWaitsForRestart', 'TestAdminSaveFailureDoesNotApply'], '保存待重启草稿与持久化失败不应用'),
 'key-http-concurrency': (SERVER, ['TestAdminGuardPersistenceAndConflict'], '配置版本冲突和保护策略持久化'),
 'key-http-status': (SERVER, ['TestAdminSlowFSHTTPKeepsStatusAndDrainResponsive'], '隔离HTTP测试中慢XML响应不阻塞状态/排空；不是高压时延SLA'),
 'key-http-readiness': (SERVER, ['TestGuardReadinessTracksAdmission'], '自有就绪状态跟随准入条件'),
 'key-http-metrics': (SERVER, ['TestControllerObservationCacheAndQueues'], '控制进程资源缓存与队列观测字段；不覆盖全部指标或生产监控'),
 'key-http-codecs': (SERVER, ['TestAudioCapabilityContractAndBound', 'TestCodecCatalogMissingBackend'], '音频能力JSON形状、输出边界和缺少原生后端的反馈；不证明转码'),
 'key-config-parameter-edit': (SERVER, ['TestFSXMLPatchAndExport', 'TestFSParameterPatchPreservesNamespacedMetadata'], 'XML参数编辑/导出及命名空间元数据保留；不执行原版配置'),
 'key-config-raw-xml': (SERVER, ['TestFSConfigSaveAndReload', 'TestFSRejectsDuplicateAttributes'], 'XML草稿保存重载与重复属性拒绝；不执行原版业务'),
 'key-config-export': (SERVER, ['TestFSXMLPatchAndExport'], '本地XML产物编辑/ZIP导出；不代表XML业务语义兼容'),
 'key-config-persistence': (SERVER, ['TestAdminBaseChangePreservesProtection'], '启动文件变化时保留并收紧已保存的保护策略'),
 'key-guard-capacity': (SERVER, ['TestGuardPeakBoundaries', 'TestGuardDisabledRetainsHardCaps'], '业务并发边界和关闭软保护时仍保留硬上限'),
 'key-guard-establishing': (SERVER, ['TestGuardPeakBoundaries', 'TestGuardLoweringLimitsPreservesAnswerACKAndBYE'], '建立中阈值与在线降限不切断已建立呼叫状态机'),
 'key-guard-rate-burst': (SERVER, ['TestGuardSmoothRefill', 'TestGuardUpdatesNeverRefillBurst', 'TestGuardRetransmissionsDoNotCharge'], '令牌平滑补充、策略更新不额外补满及重传不重复扣费'),
 'key-guard-retry-after': (SERVER, ['TestGuardRetryAfterAndRejectedRetransmission', 'TestGuardRetryAfterBeforeTokenCheck'], '过载Retry-After及被拒绝重传处理'),
 'key-guard-soft-throttle': (SERVER, ['TestGuardSmoothRefill'], '业务保护平滑降速与令牌补充的单元合同'),
 'key-sip-sdp': (SIP, ['TestAudioCodecMappingsRoundTrip', 'TestCodecMappingAndFMTPRejections', 'TestAnswerNegotiationRejectsImplicitTransforms', 'TestUnsupportedPacketDurationOnlySkipsThatCandidate'], 'Go SDP格式/时钟协商、非法参数及隐式转码拒绝；不证明真实媒体转码'),
 'key-sip-cancel': (SERVER, ['TestForkFinalRetransmissionKeepsOneCleanupTransaction'], '迟到分叉最终响应清理事务去重；不覆盖原版完整CANCEL合同'),
 'key-sip-bye': (SERVER, ['TestForkCleanupFinalResponseRequiresExactTransaction', 'TestForkCleanupStateBoundAndStatelessOverflow', 'TestForkCleanupExpiresAndCallGCRemovesItsTimers'], '分叉BYE清理身份、边界和定时器回收的自有状态机'),
 'key-ipc-connect': (SERVER, ['TestConnectUpdatesCoalesceAndKeepLatestFinalTarget', 'TestEndedCallDoesNotApplyQueuedConnect'], '媒体连接更新合并与已结束通话不应用排队更新'),
 'key-ipc-release': (MEDIA, ['TestPoolReleaseFailureEndsGeneration', 'TestPoolSubmissionQueueBoundAndClose'], '释放失败结束分片代次与有界提交队列关闭'),
 'key-ipc-allocate': (MEDIA, ['TestPoolCancellationKeepsInFlightAllocationOutcome', 'TestPoolRejectsMalformedSuccessfulReplies'], '取消时保留在途分配结果与非法成功回复拒绝'),
 'key-cli-callbench': ('rustswitch/control/cmd/callbench', ['TestSharedEndpointValidatesFlowAndTimestamp', 'TestCodecRTPClockAndLength'], '发生器共享端点归属与RTP时钟/长度校验单元合同；不表示容量通过'),
}
# 两种UDP模式都必须真实完成；这些映射只说明具体转发/拒绝/隔离断言，不推导音质或无损迁移。
E2E_CLAIMS = {
 'key-esl-sendmsg': (['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects','esl_applications_e2e.ApplicationIntegration.test_tone_playback_sends_actual_audio_then_restores_bridge','esl_applications_e2e.ApplicationIntegration.test_hangup_cancels_active_read_and_queued_mutation','esl_applications_e2e.LocalApplicationIntegration.test_local_inbound_tone_and_digits_without_upstream'],'两种UDP模式下真实set/unset/read、提示音和挂断取消；本地只A腿无需上游，原版全部应用合同未完成'),
 'key-ipc-dtmf-events': (['media_interaction_e2e.MediaInteraction.test_completed_dtmf_deduplicates_and_bridge_preserves_packets','media_interaction_e2e.MediaInteraction.test_missing_end_is_reported_and_release_cancels_pending'],'两种UDP模式下真实DTMF结束去重、缺结束报错与释放；不覆盖全部事件组合'),
 'key-ipc-playback-start': (['media_interaction_e2e.MediaInteraction.test_pcmu_tone_and_bridge_restoration','media_interaction_e2e.MediaInteraction.test_pcma_tone_and_bridge_restoration','media_interaction_e2e.MediaInteraction.test_local_a_leg_ivr_without_any_b_peer'],'两种UDP模式下真实PCMU/PCMA波形和无B腿本地播放；限单音提示'),
 'key-ipc-playback-status': (['media_interaction_e2e.MediaInteraction.test_playback_preserves_reverse_audio_and_both_dtmf_legs'],'两种UDP模式下实际播放状态、反向音频/双腿DTMF继续与CN替代'),
 'key-ipc-playback-stop': (['media_interaction_e2e.MediaInteraction.test_playback_stop_is_bounded_and_ids_are_owned'],'两种UDP模式下真实停止、幂等与任务所有权边界'),
 'key-esl-api': (['esl_e2e.ESLIntegration.test_esl_variables_do_not_interrupt_audio','esl_e2e.ESLIntegration.test_uuid_kill_hangs_up_both_legs_and_reclaims_media'], '两种UDP媒体模式下真实SIP/Rust双向RTP、ESL变量隔离、正常原因uuid_kill双腿拆线和再次呼叫'),
 'key-esl-subscriptions': (['esl_e2e.ESLIntegration.test_esl_variables_do_not_interrupt_audio','esl_e2e.ESLIntegration.test_esl_disconnect_preserves_established_call'], '真实CREATE/ANSWER/HANGUP子集与ESL断连保留现有媒体；不含完整事件字段或outbound'),


 'key-media-rtp': (['e2e.Integration.test_bidirectional_rtp_rtcp_dtmf_and_cleanup', 'e2e.Integration.test_receive_burst_across_multiple_batches', 'codec_e2e.CodecIntegration.test_relay_opus', 'codec_e2e.CodecIntegration.test_unnegotiated_payload_is_rejected'], '两种UDP模式下固定音频向量同格式RTP透传、跨批次接收及未协商载荷拒绝；不证明解码音质、转码或容量'),
 'key-media-rtcp': (['e2e.Integration.test_bidirectional_rtp_rtcp_dtmf_and_cleanup'], '两种UDP模式下测试RTCP包双向原样转发和端口归属；不覆盖原版完整RTCP统计/反馈语义'),
 'key-media-dtmf': (['e2e.Integration.test_bidirectional_rtp_rtcp_dtmf_and_cleanup', 'codec_e2e.CodecIntegration.test_dtmf_and_cn_preserve_negotiated_clocks','esl_applications_e2e.ApplicationIntegration.test_info_mode_collects_once_and_ignores_parallel_rtp','esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects'], '两种UDP模式下telephone-event/CN时钟与透传、真实read收号及INFO去重；不含音内检测或完整FreeSWITCH收号语义'),
 'key-media-failover': (['e2e.Integration.test_other_shard_survives_media_process_failure', 'e2e.Integration.test_media_worker_failure_and_restart'], '两种UDP模式下分片故障代次重启与另一分片媒体继续；受损分片通话会结束，不是活动状态无损迁移'),
 'key-sip-invite': (['e2e.Integration.test_duplicate_invite_does_not_allocate_again', 'e2e.Integration.test_early_media_before_final_answer', 'e2e.Integration.test_missing_ack_reclaims_media'], '两种UDP模式下可信中继INVITE重传分配去重、早期媒体及无ACK资源回收；不含用户注册或原版互通'),
 'key-sip-sdp': (['codec_e2e.CodecIntegration.test_invalid_and_unsupported_offers_return_488', 'codec_e2e.CodecIntegration.test_answer_cannot_request_implicit_transcoding', 'codec_e2e.CodecIntegration.test_answer_cannot_remap_dynamic_payload'], '两种UDP模式下真实SIP会话的非法SDP/不支持格式拒绝与隐式转码/PT重写拒绝；不代表实际跨格式转码'),
}
# 原版应用声明与关键入口分别保留ID；绿色只继承明确执行过的同一断言，不覆盖整个模块。
for _identifier, _tests, _scope in [
 ('registration:app:mod_dptools:set:6690',['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects'],'普通变量set的真实副作用和事件；不含数组或变量展开'),
 ('registration:app:mod_dptools:unset:6706',['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects'],'普通变量unset删除的实际效果；不含全部原版变量容器'),
 ('registration:app:mod_dptools:read:6797',['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects','esl_applications_e2e.ApplicationIntegration.test_info_mode_collects_once_and_ignores_parallel_rtp','esl_applications_e2e.ApplicationIntegration.test_read_prompt_barge_in_and_missing_end_fail_honestly','esl_applications_e2e.ApplicationIntegration.test_read_timeout_is_complete_and_unknown_application_is_rejected'],'真实RTP/INFO收号、结束去重、提示音打断、缺结束失败和超时；不含完整原版缓冲/文件/参数语义'),
 ('registration:app:mod_dptools:playback:6790',['esl_applications_e2e.ApplicationIntegration.test_tone_playback_sends_actual_audio_then_restores_bridge','esl_applications_e2e.LocalApplicationIntegration.test_local_inbound_tone_and_digits_without_upstream'],'仅PCMA/PCMU受限tone_stream真实播放与桥接恢复；不含文件/TTS播放'),
 ('event:DTMF',['esl_applications_e2e.ApplicationIntegration.test_info_mode_collects_once_and_ignores_parallel_rtp','esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects'],'真实RTP/INFO按键的通道事件、去重及收号；完整原版字段未全部对照'),
 ('event:CHANNEL_EXECUTE',['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects'],'受限应用开始事件与实际任务关联；不含全部FreeSWITCH应用路径'),
 ('event:CHANNEL_EXECUTE_COMPLETE',['esl_applications_e2e.ApplicationIntegration.test_set_unset_and_rtp_read_menu_have_real_effects','esl_applications_e2e.ApplicationIntegration.test_hangup_cancels_active_read_and_queued_mutation'],'受限应用完成、变量结果及挂断取消；不是完整原版事件字段认证'),
]:
    E2E_CLAIMS[_identifier] = (_tests, '两种UDP模式下'+_scope)


# 本轮新增入口只映射实际副作用和错误/回收断言；不按模块名批量标绿。
E2E_CLAIMS['key-config-dialplan-runtime'] = (['dialplan_e2e.XMLIntegration.test_xml_answer_sleep_order_and_park','dialplan_e2e.XMLIntegration.test_xml_unmatched_404_retransmission_ignores_legacy_local','dialplan_e2e.XMLIntegration.test_xml_answer_waits_for_ack_and_reclaims_on_missing_ack','dialplan_e2e.XMLWithoutESL.test_xml_dtmf_and_hangup_execute_without_esl'], '两种UDP模式下XML顺序、404/重传、无ACK回收和ESL关闭仍真实收号挂机；不是完整XML求值认证')
E2E_CLAIMS['registration:api:mod_commands:uuid_setvar_multi:7776'] = (['esl_variables_e2e.VariableIntegration.test_batch_variables_preserve_audio_and_leg_isolation','esl_variables_e2e.VariableIntegration.test_batch_partial_errors_limits_and_subsequent_call'], '两种UDP模式下批量变量实际读回、空值删除/分号转义、部分失败、预算、双腿媒体继续与再次呼叫；完整原版变量容器仍未实现')
for _name, _line in [('answer',6674),('sleep',6664),('park',6787),('hangup',6677)]:
    E2E_CLAIMS[f'registration:app:mod_dptools:{_name}:{_line}'] = (['dialplan_e2e.XMLIntegration.test_xml_answer_sleep_order_and_park','dialplan_e2e.XMLWithoutESL.test_xml_dtmf_and_hangup_execute_without_esl'], '两种UDP模式下实际接听等待ACK、顺序sleep、park保持和收号后真实BYE；park私有事件、全部cause和参数未完全对照')


E2E_CLAIMS['key-ipc-playback-file-start'] = (['file_playback_e2e.FilePlayback.test_pcm16_to_pcmu_actual_samples_and_tail','file_playback_e2e.FilePlayback.test_pcm16_to_pcma_actual_samples_and_tail','file_playback_e2e.FilePlayback.test_alaw_wave_to_pcma','file_playback_e2e.FilePlayback.test_mulaw_wave_to_pcmu','file_playback_e2e.FilePlayback.test_invalid_and_missing_files_never_send_audio','file_playback_e2e.FilePlayback.test_loading_stop_and_new_tone_reject_late_file','file_playback_e2e.FilePlayback.test_release_during_loading_has_no_late_audio_or_resources'], '两种UDP模式下真实WAV→G.711样本和尾帧、非法文件拒绝、loading停止/释放无迟到音频；限8k单声道/30秒')
E2E_CLAIMS['registration:app:mod_dptools:playback:6790'] = (['dialplan_e2e.XMLFileIntegration.test_xml_wav_real_packets_then_hangup','dialplan_e2e.XMLFileFailure.test_xml_invalid_wav_fails_and_never_runs_later_actions','esl_applications_e2e.ApplicationIntegration.test_tone_playback_sends_actual_audio_then_restores_bridge'], '两种UDP模式下受限WAV和单音真实播放/失败及挂机；不含原版所有文件后端、seek、循环或TTS')


E2E_CLAIMS['registration:api:mod_commands:uuid_break:7729'] = (['playback_controls_e2e.PlaybackControls.test_none_ignores_digit_then_uuid_break_confirms_silence','playback_controls_e2e.PlaybackControls.test_read_break_stops_prompt_but_waits_for_failure_timeout'], '两种UDP模式下活动playback真实停音且保留通话；read仅停提示音仍等待期限。all/both/无活动播放或任意应用break尚未兼容')
E2E_CLAIMS['registration:app:mod_dptools:playback:6790'][0].extend(['playback_controls_e2e.PlaybackControls.test_default_and_custom_terminators_record_real_key','playback_controls_e2e.PlaybackControls.test_missing_file_clears_old_terminator_and_reports_not_found'])

# 同一真实呼叫的各个断言关联回各自原始ID；不把一次运行推广为全部字段兼容。
for _name in ('CHANNEL_CREATE', 'CHANNEL_ANSWER', 'CHANNEL_HANGUP'):
    E2E_CLAIMS['event:' + _name] = (['esl_e2e.ESLIntegration.test_channel_create_answer_hangup_are_unique_and_correlated'],
        '两种UDP模式下逐腿真实创建/接听/挂机、唯一身份、SIP来源、互相关联与时序；限定基本字段，不包含完整原版生命周期')
for _name, _line in [('uuid_exists',7745),('uuid_getvar',7748),('uuid_setvar',7777)]:
    E2E_CLAIMS[f'registration:api:mod_commands:{_name}:{_line}'] = (['esl_e2e.ESLIntegration.test_basic_uuid_api_errors_mutations_and_deleted_channels'],
        '两种UDP模式下活动/已删除通道、中文变量读写删除、双腿隔离、非法输入与媒体保持；不包含全部原版变量求值与副作用')
E2E_CLAIMS['registration:api:mod_commands:uuid_kill:7750'] = (['esl_e2e.ESLIntegration.test_uuid_kill_hangs_up_both_legs_and_reclaims_media','esl_e2e.ESLIntegration.test_basic_uuid_api_errors_mutations_and_deleted_channels'],
    '两种UDP模式下NORMAL_CLEARING实际双腿BYE、资源回收及再次呼叫；非法原因拒绝且通话保留，不代表全部挂机cause')
E2E_CLAIMS['registration:api:mod_commands:uuid_dump:7744'] = (['esl_channel_snapshot_e2e.ChannelSnapshot.test_snapshot_formats_reflect_live_leg_variables_and_media','esl_channel_snapshot_e2e.ChannelSnapshot.test_snapshot_deleted_channel_errors_keep_next_call_usable'],
    '两种UDP模式下真实双腿快照、txt/plain/JSON/XML与未知格式回退、中文变量隔离、结束后查无及再次呼叫媒体；不含原版完整字段和状态机')

# 生命周期以真实音频、停止回执、挂机与故障回收作证，订阅成功不授予实现状态。
for _event in ('PLAYBACK_START', 'PLAYBACK_STOP'):
    E2E_CLAIMS['event:' + _event] = ([
        'event_lifecycle_e2e.PlaybackLifecycle.test_wav_natural_end_has_one_start_done_stop_and_real_audio',
        'event_lifecycle_e2e.PlaybackLifecycle.test_digit_stop_is_break_with_used_digit',
        'event_lifecycle_e2e.PlaybackLifecycle.test_api_break_stops_media_before_completion',
        'event_lifecycle_e2e.PlaybackLifecycle.test_hangup_waits_for_media_release_stop_then_complete',
        'event_lifecycle_e2e.PlaybackLifecycle.test_missing_wav_has_no_fake_start_or_stop',
        'event_lifecycle_e2e.PlaybackLifecycle.test_worker_termination_confirms_stop_and_reclaims_call',
        'event_lifecycle_e2e.PlaybackLifecycle.test_read_prompt_lifecycle_does_not_complete_collection_early',
        'event_lifecycle_e2e.BridgeLifecycle.test_b_leg_events_and_audio_do_not_use_a_leg_identity'],
        '两种UDP模式真实WAV/单音开始、自然结束/按键/API停音、挂机/worker故障回收与逐腿事件顺序；不含原版全部字段及loading即挂机的未观测窗口')
for _event in ('CHANNEL_PARK', 'CHANNEL_UNPARK'):
    E2E_CLAIMS['event:' + _event] = (['event_lifecycle_e2e.ParkLifecycle.test_park_enter_and_hangup_leave_before_completion'],
        '两种UDP模式下真实park进入、通话保持及挂机退出顺序；仅受限park路径，不含原版transfer/unpark全部路径')

# 按键发送覆盖真实线上报文、整批边界与故障回收；扩展API/内部IPC分别绑定自己的断言。
_DTMF_SEND_TESTS = [
    'dtmf_send_e2e.DTMFAPI.test_api_sends_actual_digits_and_reports_complete',
    'dtmf_send_e2e.DTMFAPI.test_api_rejects_bad_parameters_and_info_mode_without_packets',
    'dtmf_send_e2e.DTMFAPI.test_api_queue_and_hangup_reclaim_without_replaying_old_digits',
    'dtmf_send_e2e.DTMFBridgeAPI.test_bridged_a_and_b_legs_reject_without_disrupting_real_media',
]
E2E_CLAIMS['registration:api:mod_commands:uuid_send_dtmf:7774'] = (_DTMF_SEND_TESTS,
    '两种UDP模式下本地已ACK A腿真实发送、非法输入/INFO模式拒绝、队列满/挂机回收及桥接双腿拒绝仍保留媒体；不是全部原版发送语法或远端终端认证')
E2E_CLAIMS['key-esl-dtmf-send-status'] = (_DTMF_SEND_TESTS,
    '两种UDP模式下自有查询读回真实发送包数/完成/队列与通道归属；无同名FreeSWITCH状态API，不作为新增原版兼容命令')
E2E_CLAIMS['key-ipc-dtmf-send'] = ([
    'dtmf_send_e2e.DTMFWorker.test_all_sixteen_digits_exact_duration_and_state',
    'dtmf_send_e2e.DTMFWorker.test_playback_keeps_sequence_audio_clock_and_ssrc_across_dtmf_and_next_play',
    'dtmf_send_e2e.DTMFWorker.test_opus_48k_and_g722_8k_timestamp_clock',
    'dtmf_send_e2e.DTMFWorker.test_invalid_batch_negotiation_and_leg_never_partially_send',
    'dtmf_send_e2e.DTMFWorker.test_bridge_rejection_preserves_both_rtp_and_rtcp',
    'dtmf_send_e2e.DTMFWorker.test_activated_local_stream_rejects_connect_repeat_without_mutation',
], '两种UDP模式下16按键/精确duration/三结束包、PT110、Opus48k/G7228k、共享播放包序与恒定事件时戳；拒绝不修改桥接RTP/RTCP；仅内部媒体合同')
E2E_CLAIMS['key-ipc-dtmf-send-status'] = ([
    'dtmf_send_e2e.DTMFWorker.test_all_sixteen_digits_exact_duration_and_state',
    'dtmf_send_e2e.DTMFWorker.test_queue_limit_is_atomic_and_release_cancels',
    'dtmf_send_e2e.DTMFWorker.test_scheduler_failure_is_visible_and_does_not_stall_other_media',
], '两种UDP模式下真实完成包数、32数字队列整批拒绝、释放清理和暂停自有worker造成明确失败；completed是UDP提交，不是远端听到')

E2E_STAGES = [('sip-udp', 'e2e', False), ('sip-connected', 'e2e', True),
              ('codec-udp', 'codec_e2e', False), ('codec-connected', 'codec_e2e', True),
              ('esl-udp', 'esl_e2e', False), ('esl-connected', 'esl_e2e', True),
              ('lifecycle-udp', 'event_lifecycle_e2e', False), ('lifecycle-connected', 'event_lifecycle_e2e', True),
              ('snapshot-udp', 'esl_channel_snapshot_e2e', False), ('snapshot-connected', 'esl_channel_snapshot_e2e', True),
              ('applications-udp','esl_applications_e2e',False),('applications-connected','esl_applications_e2e',True),
              ('variables-udp','esl_variables_e2e',False),('variables-connected','esl_variables_e2e',True),
              ('playback-controls-udp','playback_controls_e2e',False),('playback-controls-connected','playback_controls_e2e',True),
              ('file-udp','file_playback_e2e',False),('file-connected','file_playback_e2e',True),
              ('dialplan-udp','dialplan_e2e',False),('dialplan-connected','dialplan_e2e',True),
              ('interaction-udp','media_interaction_e2e',False),('interaction-connected','media_interaction_e2e',True),
              ('dtmf-send-udp','dtmf_send_e2e',False),('dtmf-send-connected','dtmf_send_e2e',True)]
# 固定子进程入口：从真实TestLoader取得完整集合，再执行TextTestRunner；stdout和stderr分别留证。
E2E_RUNNER = '''import importlib,json,sys,unittest
from pathlib import Path
sys.path.insert(0,sys.argv[1])
import e2e
e2e.CONTROL=Path(sys.argv[3]);e2e.MEDIA=Path(sys.argv[4]);e2e.CONNECTED=sys.argv[5]=='connected'
module=importlib.import_module(sys.argv[2])
if sys.argv[2] in ('media_interaction_e2e','file_playback_e2e'):
    module.MEDIA=Path(sys.argv[4]);module.CONNECTED=sys.argv[5]=='connected'
if sys.argv[2] == 'dtmf_send_e2e':
    module.media_fixture.MEDIA=Path(sys.argv[4]);module.media_fixture.CONNECTED=sys.argv[5]=='connected'
suite=unittest.defaultTestLoader.loadTestsFromModule(module)
def names(suite):
    result=[]
    for test in suite:
        result.extend(names(test) if isinstance(test,unittest.TestSuite) else [test.id()])
    return result
expected=names(suite)
class Result(unittest.TextTestResult):
    def __init__(self,*args,**kwargs):
        super().__init__(*args,**kwargs);self.started=[];self.passed=[]
    def startTest(self,test):
        self.started.append(test.id());super().startTest(test)
    def addSuccess(self,test):
        self.passed.append(test.id());super().addSuccess(test)
result=unittest.TextTestRunner(verbosity=2,descriptions=False,resultclass=Result).run(suite)
print(json.dumps({'expected':expected,'started':result.started,'passed':result.passed,'tests_run':result.testsRun,'failures':len(result.failures),'errors':len(result.errors),'skipped':len(result.skipped),'successful':result.wasSuccessful()}))
sys.exit(0 if result.wasSuccessful() else 1)
'''


def require(condition, message):
    """所有门禁使用真实异常，python -O不能取消验证。"""
    if not condition:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def wire(value):
    return (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode()


def utc(value):
    """证据时刻必须显式带时区，不能用文件mtime补造执行时间。"""
    require(isinstance(value, str), '证据时刻不是字符串')
    parsed = datetime.fromisoformat(value.replace('Z', '+00:00'))
    require(parsed.tzinfo is not None, '证据缺少时区')
    return parsed.astimezone(timezone.utc)


def now_text():
    return datetime.now(timezone.utc).isoformat()


def source_snapshot(root=ROOT, runtime=False):
    """绑定Go/测试/配置、完整vendor依赖及XML模板；文档不进入功能指纹，避免自身发布循环。

    前端和文档内容不在本地Go断言绿色范围。容量证据另加入Rust/C/测试发生器依赖源码。
    """
    # 时钟依赖含平台汇编和模块选择清单；仅绑定.go会漏掉实际参与链接的代码变化。
    patterns = ['control/**/*.go', 'control/go.mod', 'control/go.sum', 'control/vendor/**/*', 'config/*.json',
                'control/internal/server/fs_templates/**/*']
    native_patterns = ['control/**/*.' + suffix for suffix in GO_NATIVE_SUFFIXES]
    if runtime:
        patterns += ['media/src/**/*.rs', 'media/tests/**/*.rs', 'media/build.rs', 'media/.cargo/*.toml', 'media/Cargo.toml', 'media/Cargo.lock',
                     'native/**/*.c', 'native/**/*.h', 'native/audio/*.py', 'native/audio/sources.json', 'tests/*.py']
    matched = {p for pattern in patterns for p in root.glob(pattern)}
    # 内嵌文档中的C头文件只是下载展示资产，不是Go/CGO编译单元，避免文档发布循环。
    matched.update(p for pattern in native_patterns for p in root.glob(pattern)
                   if not any(p.is_relative_to(root / directory) for directory in
                              ('control/internal/server/doc_assets', 'control/internal/server/web')))
    require(not any(p.is_symlink() for p in matched), '运行源码含符号链接，需要先明确实际来源再采集')
    files = {p for p in matched if p.is_file()}
    return {p.relative_to(root).as_posix(): sha(p.read_bytes()) for p in sorted(files)}


def read_artifact(base, reference):
    """只读证据目录内非符号链接的有界文件，并核对预先记录的SHA；缺失绝不补通过。"""
    require(isinstance(reference, dict), '缺少文件证据引用')
    name, expected = reference.get('path'), reference.get('sha256')
    require(isinstance(name, str) and not Path(name).is_absolute() and '..' not in Path(name).parts, '证据路径越界')
    if isinstance(base, dict):
        # 发布包内只嵌入原始只读日志，限制解压大小，源码包离线即可重新判定而非只信passed字段。
        item = base.get(name, {})
        require(item.get('encoding') == 'zlib+base64' and item.get('sha256') == expected, '内嵌证据引用不一致')
        decoder = zlib.decompressobj()
        data = decoder.decompress(base64.b64decode(item['data'], validate=True), (64 << 20) + 1)
        require(len(data) <= 64 << 20 and decoder.eof and not decoder.unused_data and not decoder.unconsumed_tail, '内嵌证据超过边界或压缩流损坏')
        require(item.get('bytes') == len(data), '内嵌证据大小不匹配')
    else:
        path = base / name
        require(path.is_file() and not path.is_symlink() and path.resolve().is_relative_to(base.resolve()), '证据文件缺失或不是普通文件')
        require(path.stat().st_size <= 64 << 20, '证据文件超过64MiB')
        data = path.read_bytes()
    require(isinstance(expected, str) and re.fullmatch('[0-9a-f]{64}', expected) and sha(data) == expected, '证据SHA不匹配')
    return data


def pack_evidence(receipt, base):
    """实际核对过的文本日志压缩随文档发布；不把大体积二进制嵌入网页。"""
    if receipt.get('kind') == rx_verification.RX_KIND:
        return rx_verification.pack_rx(receipt, base)
    refs = [receipt.get('log'), receipt.get('stderr')] if receipt.get('kind') == 'go_test_json' else list(receipt.get('artifacts', {}).values())
    result = {}
    for reference in refs:
        raw = read_artifact(base, reference)
        result[reference['path']] = {'encoding': 'zlib+base64', 'bytes': len(raw), 'sha256': sha(raw),
                                    'data': base64.b64encode(zlib.compress(raw, 9)).decode('ascii')}
    return result


def parse_go(raw):
    """同时要求run、test pass及package pass；截断、跳过、失败不能由PASS文本掩盖。"""
    rows = [json.loads(line) for line in raw.decode().splitlines() if line.strip()]
    require(rows and all(isinstance(r, dict) and isinstance(r.get('Action'), str) and isinstance(r.get('Package'), str) for r in rows), '不是Go JSON运行轨迹')
    runs, passed, failed, package_pass, package_started, package_ended = set(), set(), set(), set(), set(), set()
    for r in rows:
        action, package, name = r['Action'], r['Package'], r.get('Test')
        if action == 'start' and not name:
            package_started.add(package)
        if name:
            item = (package, name)
            if action == 'run': runs.add(item)
            elif action == 'pass':
                require(item in runs and package not in package_ended, 'test pass缺少先行run或在包结束之后')
                passed.add(item)
            elif action in {'skip', 'fail'}: failed.add(item)
        elif action in {'pass', 'skip', 'fail'}:
            require(package in package_started and package not in package_ended, '包缺少start或重复结束')
            package_ended.add(package)
            if action == 'pass': package_pass.add(package)
    # suite_failed使用fail事件，skip包可只是没有测试；具体要求测试被skip仍不授予通过。
    return {'passed': (passed & runs) - failed, 'package_pass': package_pass,
            'suite_failed': any(r['Action'] == 'fail' for r in rows),
            'complete': package_started == package_ended,
            'package_started': package_started, 'first_time': rows[0].get('Time'), 'last_time': rows[-1].get('Time'),
            'test_pass_events': len(passed), 'event_count': len(rows)}


def capture_go(evidence_dir, executable, root=ROOT):
    """真实执行一次固定race命令，执行前后采集源码；不会读取旧日志后补造版本绑定。"""
    evidence_dir.mkdir(parents=True, exist_ok=True)
    identifier = 'go-' + uuid.uuid4().hex
    before = source_snapshot(root)
    started = now_text()
    command = [str(executable), *GO_COMMAND[1:]]
    log, diagnostic = evidence_dir / (identifier + '.jsonl'), evidence_dir / (identifier + '.stderr')
    with log.open('xb') as out, diagnostic.open('xb') as err:
        try:
            result = subprocess.run(command, cwd=root / 'control', stdout=out, stderr=err, timeout=300, check=False)
            code, timed_out = result.returncode, False
        except subprocess.TimeoutExpired:
            code, timed_out = 124, True
    receipt = {'schema_version': '1.0.0', 'kind': 'go_test_json', 'id': identifier, 'started_at': started, 'finished_at': now_text(),
               'command': GO_COMMAND, 'returncode': code, 'timed_out': timed_out,
               'binding_method': 'source_before_and_after_actual_execution', 'source_before': before,
               'source_after': source_snapshot(root), 'tool_sha256': sha(executable.read_bytes()),
               'recorder_sha256': sha(Path(__file__).read_bytes()),
               'log': {'path': log.name, 'sha256': sha(log.read_bytes())},
               'stderr': {'path': diagnostic.name, 'sha256': sha(diagnostic.read_bytes())}}
    (evidence_dir / (identifier + '.receipt.json')).write_bytes(wire(receipt))
    return identifier, code


def capture_e2e(evidence_dir, executable, control, media, build_receipt=None, root=ROOT):
    """固定顺序真正执行二十四组隔离用例；超时只终止自己创建的进程组并保留失败。"""
    evidence_dir.mkdir(parents=True, exist_ok=True)
    identifier, started, before = 'e2e-' + uuid.uuid4().hex, now_text(), source_snapshot(root, True)
    binaries, artifacts, stages = {}, {}, []
    for role, path in [('control', control), ('media', media)]:
        target = evidence_dir / (identifier + '-' + role + '.bin')
        shutil.copyfile(path, target)
        binaries[role] = {'path': target.name, 'sha256': sha(target.read_bytes())}
    if build_receipt:
        raw = build_receipt.read_bytes()
        path = evidence_dir / (identifier + '-build.json')
        path.write_bytes(raw)
        artifacts['build'] = {'path': path.name, 'sha256': sha(raw)}
    for name, suite, connected in E2E_STAGES:
        stdout, stderr = evidence_dir / (identifier + '-' + name + '.stdout'), evidence_dir / (identifier + '-' + name + '.stderr')
        stage = {'name': name, 'suite': suite, 'connected': connected, 'started_at': now_text()}
        command = [str(executable), '-c', E2E_RUNNER, str(root / 'tests'), suite, str(control), str(media), 'connected' if connected else 'udp']
        with stdout.open('xb') as out, stderr.open('xb') as err:
            process = subprocess.Popen(command, cwd=root, stdout=out, stderr=err, start_new_session=True)
            try:
                stage['returncode'], stage['timed_out'] = process.wait(timeout=180), False
            except subprocess.TimeoutExpired:
                stage['returncode'], stage['timed_out'] = 124, True
            finally:
                # 只针对本次start_new_session产生的进程组；任何残留也阻止标绿。
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                    stage['forced_cleanup'] = True
                except ProcessLookupError:
                    stage['forced_cleanup'] = False
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=3)
                if stage['forced_cleanup']:
                    try: os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError: pass
        stage['finished_at'] = now_text()
        for kind, path in [('stdout', stdout), ('stderr', stderr)]:
            stage[kind] = {'path': path.name, 'sha256': sha(path.read_bytes())}
            artifacts[name + '-' + kind] = stage[kind]
        stages.append(stage)
    receipt = {'schema_version': '1.0.0', 'kind': 'python_unittest_e2e', 'id': identifier,
               'started_at': started, 'finished_at': now_text(), 'binding_method': 'source_before_and_after_actual_execution',
               'source_before': before, 'source_after': source_snapshot(root, True), 'binaries': binaries,
               'binaries_after': {'control': sha(control.read_bytes()), 'media': sha(media.read_bytes())},
               'runner_source_sha256': sha(E2E_RUNNER.encode()), 'tool_sha256': sha(executable.read_bytes()),
               'artifacts': artifacts, 'stages': stages}
    (evidence_dir / (identifier + '.receipt.json')).write_bytes(wire(receipt))
    return identifier, 1 if any(s['returncode'] or s['timed_out'] or s['forced_cleanup'] for s in stages) else 0


def parse_unittest(stdout, stderr):
    """同时核对Runner的真实run/success集合与完整原始unittest输出，不接受--list或只有OK。"""
    result, raw = json.loads(stdout), stderr.decode()
    require(isinstance(result, dict), '缺少unittest实际执行摘要')
    expected, started, passed = (result.get(key) for key in ['expected', 'started', 'passed'])
    require(all(isinstance(v, list) and v and all(isinstance(name, str) for name in v) and len(v) == len(set(v)) for v in [expected, started, passed]), '用例集合缺失或重复')
    successes = re.findall(r'^test_\w+ \(([A-Za-z0-9_.]+)\) \.\.\. ok$', raw, re.M)
    total = re.search(r'\nRan (\d+) tests? in ', raw)
    require(total and int(total[1]) == result.get('tests_run') == len(expected), '用例执行数量不一致')
    require(set(expected) == set(started) == set(passed) == set(successes) and len(successes) == len(expected), '必需用例未真实全部执行成功')
    require(result.get('successful') is True and all(type(result.get(k)) is int and result[k] == 0 for k in ['failures', 'errors', 'skipped'])
            and raw.rstrip().endswith('\nOK'), 'unittest包含失败、跳过或截断')
    return set(passed)


def evaluate_e2e(receipt, base, current, as_of, identifier, claim, verify_binaries=True):
    """本地真实SIP/媒体断言绑定构建来源，六组完整成功后才授予限定范围通过。"""
    names, scope = claim
    status, reason = binding_status(receipt, current, as_of)
    proof = {'id': receipt.get('id', 'invalid') + ':' + identifier, 'comparison_id': identifier,
             'kind': 'python_unittest_e2e', 'scope': scope, 'execution_id': receipt.get('id'),
             'test_names': [mode + ':' + name for mode in ['udp', 'connected'] for name in names],
             'status': status, 'reason': reason, 'executed': False, 'green_eligible': False,
             'started_at': receipt.get('started_at'), 'finished_at': receipt.get('finished_at'),
             'source_snapshot_sha256': sha(wire(receipt.get('source_after', {})))}
    try:
        require(receipt.get('runner_source_sha256') == sha(E2E_RUNNER.encode()), '不是固定的完整E2E执行入口')
        stages = receipt['stages']
        require([(s['name'], s['suite'], s['connected']) for s in stages] == E2E_STAGES, '缺少固定二十四组端到端执行')
        proof['log'] = stages[0]['stderr']
        passed = {'udp': set(), 'connected': set()}
        for stage in stages:
            require(utc(receipt['started_at']) <= utc(stage['started_at']) <= utc(stage['finished_at']) <= utc(receipt['finished_at']), 'E2E阶段超出执行窗口')
            require(type(stage.get('returncode')) is int and stage['returncode'] == 0 and stage.get('timed_out') is False
                    and stage.get('forced_cleanup') is False, '端到端阶段退出失败、超时或需强制清理')
            tests = parse_unittest(read_artifact(base, stage['stdout']), read_artifact(base, stage['stderr']))
            passed['connected' if stage['connected'] else 'udp'].update(tests)
        require(all(set(names) <= got for got in passed.values()), '两种UDP模式没有全部必需断言')
        proof['executed'] = True
        if 'build' not in receipt.get('artifacts', {}):
            proof.update(status='unbound', reason='真实E2E已运行，但缺少执行二进制的实际构建前后源码收据')
            return proof
        build = json.loads(read_artifact(base, receipt['artifacts']['build']))
        require(build.get('schema_version') == '1.0.0' and build.get('kind') == 'local_runtime_build'
                and type(build.get('returncode')) is int and build['returncode'] == 0, '缺少成功的实际构建收据')
        require(build.get('source_before') == build.get('source_after') == receipt['source_before'], '二进制构建源与E2E运行源不一致')
        require(utc(build['started_at']) <= utc(build['finished_at']) <= utc(receipt['started_at']), '构建时刻不早于运行或无效')
        for role in ['control', 'media']:
            expected = receipt['binaries'][role]['sha256']
            require(build['binaries'][role]['sha256'] == receipt['binaries_after'][role] == expected, '二进制不匹配或在运行中变化')
            if verify_binaries: check_runtime_binary(base, receipt['binaries'][role])
        proof['verified_binary_sha256'] = {role: receipt['binaries'][role]['sha256'] for role in ['control', 'media']}
        if status == 'passed':
            proof['green_eligible'] = True
            proof['expires_at'] = (utc(receipt['finished_at']) + timedelta(days=LIFETIME_DAYS)).isoformat()
    except (OSError, UnicodeError, TypeError, KeyError, ValueError, zlib.error) as error:
        proof.update(status='failed', reason='E2E证据未满足完整门禁：' + str(error), green_eligible=False)
    return proof


def binding_status(receipt, current, as_of):
    """源码新增、删除、变动以及执行窗口内变动都使证据失效；30天为显式复核期限。"""
    if receipt.get('binding_method') not in {'source_before_and_after_actual_execution', 'source_before_build_and_after_run'}:
        return 'unbound', '缺少执行前后源码绑定'
    before, after = receipt.get('source_before'), receipt.get('source_after')
    if not isinstance(before, dict) or not before or not isinstance(after, dict):
        return 'unbound', '缺少执行时源码清单'
    if before != after or after != current:
        return 'stale', '执行中或执行后源码集合/内容发生变化，需要重新运行'
    try:
        started, finished = utc(receipt['started_at']), utc(receipt['finished_at'])
        require(started <= finished <= as_of + timedelta(days=1), '执行时刻顺序无效')
        if as_of >= finished + timedelta(days=LIFETIME_DAYS):
            return 'stale', '超过30天复核期限'
    except (ValueError, TypeError, KeyError):
        return 'missing_evidence', '执行时刻缺失或无效'
    return 'passed', '源码集合与日志指纹均匹配；仅授予所列本地断言范围'


def evaluate_go(receipt, base, current, as_of, identifier, claim):
    """一个声明的所有必需测试必须在同一次完整成功执行中出现。"""
    package, names, scope = claim
    status, reason = binding_status(receipt, current, as_of)
    result = {'id': receipt.get('id', 'invalid') + ':' + identifier, 'comparison_id': identifier, 'kind': 'go_race_subset',
              'scope': scope, 'execution_id': receipt.get('id'), 'test_names': [package + ':' + name for name in names],
              'status': status, 'reason': reason, 'executed': False, 'source_snapshot_sha256': sha(wire(receipt.get('source_after', {}))),
              'started_at': receipt.get('started_at'), 'finished_at': receipt.get('finished_at'),
              'log': receipt.get('log'), 'green_eligible': False}
    try:
        require(receipt.get('command') == GO_COMMAND and isinstance(receipt.get('tool_sha256'), str)
                and re.fullmatch('[0-9a-f]{64}', receipt['tool_sha256']), '不是固定的真实Go race采集命令')
        trace = parse_go(read_artifact(base, receipt.get('log')))
        read_artifact(base, receipt.get('stderr'))
        require(utc(receipt['started_at']) <= utc(trace['first_time']) <= utc(trace['last_time']) <= utc(receipt['finished_at']), 'Go轨迹不在本次执行时窗内')
        result['executed'] = all((package, name) in trace['passed'] for name in names)
        if type(receipt.get('returncode')) is not int or receipt['returncode'] != 0 or receipt.get('timed_out') is not False or trace['suite_failed']:
            result.update(status='failed', reason='本次Go执行退出失败、到期或包含失败测试')
        elif not trace['complete'] or not result['executed'] or package not in trace['package_pass'] or package not in trace['package_started']:
            result.update(status='not_run', reason='同一次完整成功包执行中没有全部必需测试的run/pass事件')
        elif status == 'passed':
            result['green_eligible'] = True
            result['expires_at'] = (utc(receipt['finished_at']) + timedelta(days=LIFETIME_DAYS)).isoformat()
    except (ValueError, TypeError, KeyError, UnicodeError, OSError, zlib.error):
        result.update(status='missing_evidence', reason='运行日志/诊断文件缺失、损坏、截断或SHA不匹配')
    return result


def processed_refs(raw):
    """只枚举原收据实际引用的文件；不补造原来没有的状态、日志或源码快照。"""
    refs = {raw['build_path']: {'path': raw['build_path'], 'sha256': raw['build_sha256']}}
    for stage in raw['stages']:
        for reference in [stage['log'], *stage['artifacts']]:
            previous = refs.setdefault(reference['path'], {'path': reference['path'], 'sha256': reference['sha256']})
            require(previous['sha256'] == reference['sha256'], '同路径的原始证据 SHA 冲突')
        for case in stage['artifacts']:
            for path, item in case['artifacts'].items():
                previous = refs.setdefault(path, {'path': path, 'sha256': item['sha256']})
                require(previous['sha256'] == item['sha256'], '同路径的用例证据 SHA 冲突')
    return refs


def import_processed(receipt_path, build_path, control, media, evidence_dir, root=ROOT):
    """归档已执行的独立 M1 收据，原文件逐字节保存；导入时刻绝不充当构建/运行时刻。

    official build 必须由调用方提供当时已采集的收据；本函数不创建任何 before 快照。
    这里验证文件真实性和哈希，不把旧源测试升级为当前源测试，最终状态由 evaluate_processed 决定。
    """
    original_bytes = receipt_path.read_bytes()
    original = json.loads(original_bytes)
    require(original.get('schema_version') == 1 and original.get('kind') == PROCESSED_KIND, '不是独立 M1 原始运行收据')
    identifier = 'processed-' + uuid.uuid4().hex
    destination = evidence_dir / identifier
    destination.mkdir(parents=True, exist_ok=False)
    artifacts = {}
    def save(key, relative, data):
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
        artifacts[key] = {'path': target.relative_to(evidence_dir).as_posix(), 'sha256': sha(data)}
    save('$receipt', 'original.receipt.json', original_bytes)
    save('$build', 'official-runtime-build.json', build_path.read_bytes())
    for key, reference in processed_refs(original).items():
        save(key, 'raw/' + key, read_artifact(receipt_path.parent, reference))
    binaries = {}
    for role, source in [('control', control), ('media', media)]:
        require(source.is_file() and not source.is_symlink() and 0 < source.stat().st_size <= 512 << 20,
                '导入需要有界的实际普通二进制文件')
        data = source.read_bytes()
        target = destination / ('binary-' + role)
        target.write_bytes(data)
        binaries[role] = {'path': target.relative_to(evidence_dir).as_posix(), 'sha256': sha(data)}
    envelope = {'schema_version': '1.0.0', 'id': identifier, 'kind': PROCESSED_KIND,
                'started_at': original['started_at'], 'finished_at': original['finished_at'],
                'artifacts': artifacts, 'binaries': binaries}
    (evidence_dir / (identifier + '.receipt.json')).write_bytes(wire(envelope))
    return identifier


def processed_source_binding(raw, build, current):
    """完整新合同优先；旧合同只投影当时确实采集的 Go/Rust/测试，明确保留边界。

    全部 native/config 仍受真实构建前后清单与当前源码约束。不能向旧运行清单填入它们，
    也不能按“与当前交集”过滤新增/删除的 Go/Rust/测试，从而错误地接受陈旧结果。
    """
    built = build['source_before']
    require(isinstance(built, dict) and built and built == build.get('source_after'), '构建前后功能源码变化或缺失')
    require(all(isinstance(p, str) and isinstance(v, str) and re.fullmatch('[0-9a-f]{64}', v)
                for p, v in built.items()), '构建功能源码清单形状错误')
    for path in ('tests/media_processed_e2e.py', 'tests/media_processed_fixture.py', 'tests/e2e.py', 'tests/esl_e2e.py'):
        require(path in built, '构建缺少 M1 完整执行器或其依赖源码')
    before, after = raw.get('source_before'), raw.get('source_after')
    require(isinstance(before, dict) and before and isinstance(after, dict), '缺少实际运行源码清单')
    tests_before, tests_after = raw.get('test_sources_before'), raw.get('test_sources_after')
    expected_tests = {p: v for p, v in built.items() if p.startswith('tests/') and p.count('/') == 1 and p.endswith('.py')}
    require(tests_before == tests_after == expected_tests, '实际测试源码清单缺项或执行期间改变')
    if 'runtime_source_before' in raw or 'runtime_source_after' in raw:
        require(isinstance(raw.get('runtime_source_before'), dict) and isinstance(raw.get('runtime_source_after'), dict), '完整功能源码运行合同不能降级为旧投影')
        same = raw['runtime_source_before'] == raw['runtime_source_after'] == built == current
        return same, 'official_full_build_and_run_snapshots'
    def captured_function(path):
        return ((path.startswith('control/') and not path.startswith(('control/internal/server/doc_assets/', 'control/internal/server/web/')))
                or path.startswith('media/') or (path.startswith('tests/') and path.count('/') == 1 and path.endswith('.py')))
    expected = {p: v for p, v in built.items() if captured_function(p)}
    first = {p: v for p, v in before.items() if captured_function(p)}
    last = {p: v for p, v in after.items() if captured_function(p)}
    require(first == last == expected, '旧运行 Go/Rust/测试投影缺项或执行期间改变')
    return built == current, 'legacy_actual_go_rust_tests_plus_official_build_native_config'


def check_runtime_binary(base, reference):
    """内嵌文档使控制二进制可能超过日志 64MiB 上限；独立流式核对，绝不内嵌二进制。"""
    name = reference['path']
    require(isinstance(name, str) and not Path(name).is_absolute() and '..' not in Path(name).parts, '二进制路径越界')
    path = base / name
    require(path.is_file() and not path.is_symlink() and path.resolve().is_relative_to(base.resolve())
            and 0 < path.stat().st_size <= 512 << 20, '二进制不是有界普通归档文件')
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''): digest.update(block)
    require(digest.hexdigest() == reference['sha256'], '实际二进制 SHA 不一致')


def processed_sip(raw):
    """有界重组 SIP，逐包核对 Content-Length；日志中的成功文字不能替代完整原报文。"""
    head, split, body = raw.partition(b'\r\n\r\n')
    require(split and len(raw) <= 65535, 'SIP 原报文截断或过大')
    lines = head.decode().split('\r\n')
    headers = {}
    for line in lines[1:]:
        key, colon, value = line.partition(':')
        require(colon and key.lower() not in headers, 'SIP 头部损坏或重复')
        headers[key.lower()] = value.strip()
    require(int(headers['content-length']) == len(body) and headers.get('call-id'), 'SIP 长度或呼叫身份不一致')
    return lines[0], headers, body


def processed_rtp(raw):
    """M1 夹具固定无扩展 RTP；离线重放不把 RTCP 或任意字节当成音频。"""
    require(len(raw) >= 12 and raw[0] == 0x80, 'M1 RTP 头部截断或偏离夹具格式')
    return {'pt': raw[1] & 127, 'seq': int.from_bytes(raw[2:4], 'big'),
            'ts': int.from_bytes(raw[4:8], 'big'), 'ssrc': int.from_bytes(raw[8:12], 'big'), 'body': raw[12:]}


def processed_pcm(body, law):
    """离线独立展开 G.711 码字，防止只改 PT 或固定静音伪装实际转码。"""
    result = []
    for code in body:
        if law == 'PCMA':
            value = code ^ 0x55
            segment = (value >> 4) & 7
            magnitude = ((value & 15) << 4) + (8 if segment == 0 else 264)
            if segment > 1: magnitude <<= segment - 1
            result.append(magnitude if value & 128 else -magnitude)
        else:
            value = (~code) & 255
            magnitude = ((((value & 15) << 3) + 132) << ((value >> 4) & 7)) - 132
            result.append(-magnitude if value & 128 else magnitude)
    return result


def processed_pcm_match(original, actual, source_law, target_law):
    require(len(original) == len(actual) == 160, 'G.711 必须实际为 20ms/160 个样本')
    source, output = processed_pcm(original, source_law), processed_pcm(actual, target_law)
    energy = sum(v * v for v in source)
    require(energy > 1_000_000 and sum((a-b)**2 for a, b in zip(source, output)) / energy < 0.003,
            '原始接收载荷不是真实源音频的目标 G.711 量化结果')


def check_processed_wire(document, config, journal, server_log, binaries, connected, test, started, finished):
    """重放所有场景的原报文和回收，另独立重算转码样本与暂停后期限，不执行证据内代码。"""
    require(document.get('test') == '__main__.' + test and document.get('connected') is connected
            and document.get('processing') == 'g711', '用例/模式/处理图身份错误')
    require(document.get('binaries_before') == document.get('binaries_after') == binaries
            and document.get('binaries_unchanged') is True, '单用例运行二进制发生变化')
    require(config['media']['processing'] == 'g711' and config['media']['connect_sockets'] is connected
            and config['media']['workers'] == 1 and config['limits']['max_calls'] == 1, '实际配置不是有界 M1 双腿夹具')
    require(b'RustSwitch ready' in server_log and b'draining existing calls' in server_log, '缺少真实启动/有序回收日志')
    events = [json.loads(line) for line in journal.splitlines()]
    require(events and events[0]['kind'] == 'controller_started', '控制器事件轨迹缺失')
    times = [utc(event['time']) for event in events]
    require(times == sorted(times) and started <= times[0] <= times[-1] <= finished, '用例事件超出实际执行窗口')
    require(len({event['run_id'] for event in events}) == 1, '混入其他进程的事件轨迹')
    rows = document['wire']
    require(isinstance(rows, list) and 0 < len(rows) <= 4096, '原始报文集合缺失或超过固定预算')
    packets, sip, signals, rtcp, catalogs = {}, [], {}, {}, []
    previous = -1
    for row in rows:
        at = row['at_ms']
        require(type(at) in (int, float) and math.isfinite(at) and previous <= at <= 30_000, '原报文时序倒退或超出单例期限')
        previous = at
        require(isinstance(row['hex'], str) and len(row['hex']) <= 131070, '原报文长度无效')
        data = bytes.fromhex(row['hex'])
        require(data and data.hex() == row['hex'], '原报文编码损坏')
        direction = row['direction']
        if direction == 'runtime_codec_catalog':
            require(test == PROCESSED_TESTS[1] and row['peer'] == ['http', 0] and not catalogs, '运行目录只能来自指定双向场景的一次实际响应')
            catalog = json.loads(data)
            runtime = catalog['runtime_media']
            require(catalog.get('media_mode') == 'g711_audio_graph' and catalog.get('live_transcoding') is True
                    and runtime.get('processing') == 'g711' and runtime.get('enabled') is True
                    and runtime.get('ready') is True and runtime.get('available') is True
                    and runtime.get('state') == 'ready' and runtime.get('capability') == 'processed_g711_v1',
                    '目录用静态 SDK 或未就绪进程冒充实时 G.711 能力')
            require(runtime.get('expected_workers') == runtime.get('healthy_workers') == runtime.get('capable_workers') == 1
                    and len(runtime['workers']) == 1 and runtime['workers'][0].get('healthy') is True
                    and runtime['workers'][0].get('generation') == 1 and type(runtime['workers'][0].get('pid')) is int
                    and runtime['workers'][0]['pid'] > 1 and 'processed_g711_v1' in runtime['workers'][0]['capabilities'],
                    '目录缺少当前 worker 代次的实际握手')
            require(started <= utc(runtime['observed_at']) <= finished and runtime.get('sample_rate') == 8000
                    and runtime.get('frame_ms') == 20 and runtime.get('jitter_target_ms') == 40 and runtime.get('max_delay_ms') == 120,
                    '实时目录时刻或处理图参数错误')
            profiles = catalog['live_codec_profiles']
            require(len(profiles) == 2 and {p['payload'] for p in profiles} == {0, 8}
                    and all(p['name'] == ('PCMU' if p['payload'] == 0 else 'PCMA') and p['sample_rate'] == p['rtp_clock_rate'] == 8000
                            and p['channels'] == 1 and p['ptime_ms'] == 20 for p in profiles), '实时目录扩大为未实现的 Codec 或帧长')
            catalogs.append(catalog)
            continue
        if direction in ('owned_worker_SIGSTOP', 'owned_worker_SIGCONT'):
            require(direction not in signals and data.decode().isdigit() and row['peer'] == ['pid', int(data)], '故障注入身份损坏')
            signals[direction] = at
            continue
        require(len(row['peer']) == 2 and row['peer'][0] == '127.0.0.1'
                and type(row['peer'][1]) is int and 0 < row['peer'][1] < 65536, '原报文不属于隔离本地端点')
        if direction in ('sip_send', 'sip_receive'):
            first, headers, body = processed_sip(data)
            sip.append((direction, first, headers, body, at))
        elif re.fullmatch(r'rtp_(send|receive)_[ab]|rtp_stale_source', direction):
            packet = processed_rtp(data)
            require((len(packet['body']) == 160 and packet['pt'] not in (13, 101))
                    or (packet['pt'] == 13 and len(packet['body']) == 1)
                    or (packet['pt'] == 101 and len(packet['body']) == 4), 'RTP 载荷类型/实际长度不一致')
            packets.setdefault(direction, []).append(dict(packet, at=at))
        elif re.fullmatch(r'rtp_(send|receive)_[ab]_rtcp|rtcp_after_hangup_[ab]', direction):
            # 复合 RTCP 必须完整对齐；随后 SR/SDES/BYE 场景仍由已绑定完整断言检查字段语义。
            cursor = 0
            while cursor < len(data):
                require(cursor + 4 <= len(data) and data[cursor] >> 6 == 2, 'RTCP 报文截断')
                size = (int.from_bytes(data[cursor+2:cursor+4], 'big') + 1) * 4
                require(size >= 4 and cursor + size <= len(data) and 200 <= data[cursor+1] <= 207, 'RTCP 长度或类型错误')
                rtcp.setdefault(direction, []).append(data[cursor+1])
                cursor += size
        else:
            raise ValueError('未知原报文方向：' + str(direction))
    rejected = test == PROCESSED_REJECT
    if rejected:
        require(not packets and len(sip) == 9 and len(events) == 1, '分配前拒绝却产生媒体、接通事件或额外 SIP')
        for offset, spec in zip(range(0, 9, 3), [('G722', 20), ('PCMU', 10), ('PCMA', 30)]):
            offer, reject, ack = sip[offset:offset+3]
            require(offer[0] == 'sip_send' and offer[1].startswith('INVITE ') and reject[0] == 'sip_receive'
                    and reject[1].startswith('SIP/2.0 488 ') and ack[0] == 'sip_send' and ack[1].startswith('ACK ')
                    and offer[2]['call-id'] == reject[2]['call-id'] == ack[2]['call-id'], '缺少对应的 INVITE/488/ACK 拒绝事务')
            require((spec[0] + '/8000').encode() in offer[3] and ('a=ptime:' + str(spec[1])).encode() in offer[3], '拒绝负例输入被替换')
        # 旧原帧确实未存 final_status，不能补 0。完整测试源码及成功日志仍包含逐例 wait_status 断言。
        if 'rejection_statuses' in document:
            require(len(document['rejection_statuses']) == 3, '新拒绝状态证据不完整')
            for state in document['rejection_statuses']:
                check_processed_zero(state, started, finished, minimum_sample=times[0])
                observed = state.get('observed_at')
                require(type(observed) in (int, float) and math.isfinite(observed)
                        and started.timestamp() <= observed <= finished.timestamp()
                        and utc(state['workers'][0]['admission']['sampled_at']).timestamp() <= observed,
                        '拒绝状态缺少实际观测时刻或采样时间倒置')
        else:
            require(document.get('final_status') is None, '旧拒绝场景不得补造回收状态')
        return {'connected_case': False, 'rejection_status_recorded': 'rejection_statuses' in document}
    calls = {}
    for event in events[1:]: calls.setdefault(event['call_id'], []).append(event)
    expected_calls = 2 if test == PROCESSED_TESTS[3] else 1
    require(len(calls) == expected_calls, '缺少实际通话周期，尤其端口复用后的第二通')
    for call_id, trace in calls.items():
        require([event['kind'] for event in trace] == ['call_admitted', 'call_answered', 'call_established', 'call_ended']
                and trace[-1].get('reason') == 'normal_hangup', '真实呼叫没有完整接通/正常挂机生命周期')
        for direction, prefix, cseq in [('sip_send', 'INVITE ', 'INVITE'), ('sip_receive', 'SIP/2.0 200 ', 'INVITE'),
                                         ('sip_send', 'ACK ', 'ACK'), ('sip_send', 'BYE ', 'BYE'), ('sip_receive', 'SIP/2.0 200 ', 'BYE')]:
            require(any(d == direction and first.startswith(prefix) and h['call-id'] == call_id
                        and h['cseq'].endswith(' ' + cseq) for d, first, h, _, _ in sip), '缺少真实 A 腿 SIP 接通/释放事务')
    require(sum(first.startswith('BYE ') and direction == 'sip_receive' for direction, first, *_ in sip) == expected_calls,
            '缺少真实 B 腿 BYE')
    state = document['final_status']
    check_processed_zero(state, started, finished, minimum_sample=times[-1])
    stats = state['workers'][0]['stats']
    require(all(type(value) is int and value >= 0 for key, value in stats.items()
                if key.startswith('processed_') and key != 'processed_output_lateness_buckets'), '处理统计不是非负整数')
    require(stats['processed_decoded_frames'] > 0 and stats['processed_decoded_samples'] == stats['processed_decoded_frames'] * 160
            and stats['processed_encoded_frames'] >= stats['processed_decoded_frames']
            and stats['processed_encoded_samples'] == stats['processed_encoded_frames'] * 160,
            '真实处理样本统计不自洽')
    require(stats['processed_send_errors'] == 0 and stats['processed_send_deadline_misses'] == 0
            and 0 <= stats['processed_output_lateness_ns_max'] < 20_000_000, '发送失败或超期不能标绿')
    buckets = stats['processed_output_lateness_buckets']
    require(isinstance(buckets, list) and len(buckets) == 8 and all(type(v) is int and v >= 0 for v in buckets)
            and sum(buckets) >= stats['processed_encoded_frames'] and buckets[-1] == 0, '发送期限直方图缺失或出现过期发送')
    for side in ('a', 'b'):
        require(packets.get('rtp_send_' + side) and packets.get('rtp_receive_' + side), '缺少某方向真实输入或接收媒体')
    expected_decoded = [48, 64, 56, 48, 50, 48, 60, 59, None, 800, None][PROCESSED_TESTS.index(test)]
    if expected_decoded is not None:
        require(stats['processed_decoded_frames'] == expected_decoded, '固定场景实际解码数不足或重复解码')
    if test == PROCESSED_TESTS[2]:
        require(stats['processed_jitter_reordered'] >= 1 and stats['processed_jitter_duplicates'] >= 1, '没有实际覆盖重排与重复')
    if test == PROCESSED_TESTS[7]:
        require(stats['processed_jitter_lost'] >= 1 and stats['processed_jitter_late'] >= 1 and stats['processed_plc_frames'] >= 1, '没有实际覆盖丢帧/迟到/PLC')
    if test in (PROCESSED_TESTS[0], PROCESSED_TESTS[6]):
        source_events = [p for p in packets['rtp_send_a'] if p['pt'] == 101]
        output_events = [p for p in packets['rtp_receive_b'] if p['pt'] == 101]
        require(len(source_events) == len(output_events) == (3 if test == PROCESSED_TESTS[0] else 5)
                and [p['body'] for p in source_events] == [p['body'] for p in output_events]
                and len({p['ts'] for p in output_events}) == 1, 'DTMF 辅助媒体没有真实完整重封装')
    if test in (PROCESSED_TESTS[4], PROCESSED_TESTS[6]):
        require(len([p for p in packets['rtp_receive_b'] if p['pt'] == 13]) == stats['processed_cn_packets'] == 1, '没有实际覆盖 CN')
    if test == PROCESSED_TESTS[9]:
        for side in ('a', 'b'):
            require({200, 202} <= set(rtcp.get('rtp_receive_' + side + '_rtcp', []))
                    and {202, 203} <= set(rtcp.get('rtcp_after_hangup_' + side, [])), '缺少双方真实 SR/SDES 或挂机复合 BYE')
    # 两个独立编码协商场景完整重放 PCM；其余故障/辅助场景保留固定完整测试断言和原帧。
    if test in (PROCESSED_TESTS[1], PROCESSED_TESTS[5], PROCESSED_TESTS[10]):
        dynamic = test == PROCESSED_TESTS[5]
        laws = {'a': 'PCMA' if dynamic else 'PCMU', 'b': 'PCMU' if dynamic else 'PCMA'}
        for side in ('a', 'b'):
            source_side = 'b' if side == 'a' else 'a'
            sent = packets['rtp_send_' + source_side]
            received = packets['rtp_receive_' + side]
            require(len({p['ssrc'] for p in received}) == 1 and received[0]['ssrc'] not in {p['ssrc'] for p in sent}, '媒体仍透传原 SSRC')
            require(all((b['seq'] - a['seq']) & 65535 == 1 for a, b in zip(received, received[1:])), '生成 RTP 序号重复、回退或有缺口')
            require(all(p['pt'] == (96 if dynamic and side == 'a' else 0 if laws[side] == 'PCMU' else 8) for p in received), '输出 PT 与独立协商不一致')
            if test != PROCESSED_TESTS[10]:
                require(len(sent) == (24 if dynamic else 32) and len(sent) <= len(received) <= len(sent) + 6, '真实音频数量或尾部 PLC 超过范围')
                for original, actual in zip(sent, received): processed_pcm_match(original['body'], actual['body'], laws[source_side], laws[side])
                require(all((b['ts']-a['ts']) & 0xffffffff == 160 for a, b in zip(received, received[1:])), '音频时间戳没有按 20ms 连续推进')
                require(30 <= received[0]['at']-sent[0]['at'] < 120, '实际首包不在固定缓冲窗口')
            else:
                require(set(signals) == {'owned_worker_SIGSTOP', 'owned_worker_SIGCONT'}
                        and 130 <= signals['owned_worker_SIGCONT']-signals['owned_worker_SIGSTOP'] < 200, '没有真实执行 140ms 暂停故障')
                require(len(sent) == 48, '暂停期间发生器没有完整发送 48 个音频包')
                positions = [((p['ts']-received[0]['ts']) & 0xffffffff) // 160 for p in received]
                require(all(((p['ts']-received[0]['ts']) & 0xffffffff) % 160 == 0 for p in received)
                        and positions == sorted(set(positions)) and 47 in positions
                        and any(b-a > 1 for a,b in zip(positions, positions[1:])), '暂停后重放旧音频、错误重锚或没有继续处理')
                for position, actual in zip(positions, received):
                    due = sent[0]['at'] + position*20 + 40
                    if actual['at'] >= signals['owned_worker_SIGCONT']:
                        require(-10 <= actual['at'] - due < 30, '暂停恢复后改期补发超过期限的旧音频')
                    if position < 48 and (actual['at'] < signals['owned_worker_SIGSTOP'] or due >= signals['owned_worker_SIGCONT']):
                        processed_pcm_match(sent[position]['body'], actual['body'], laws[source_side], laws[side])
                require(sum(signals['owned_worker_SIGCONT'] <= p['at'] < signals['owned_worker_SIGCONT']+15 for p in received) <= 2,
                        '暂停恢复集中补发，违反固定实时预算')
    return {'connected_case': True, 'call_cycles': len(calls), 'runtime_catalog_observed': bool(catalogs)}


def check_processed_zero(state, started, finished, minimum_sample):
    """只接受真实采样的空闲状态；不以缺字段、旧统计或主控清零代替媒体回收。"""
    require(state['active_calls'] == state['established_calls'] == 0
            and state['controller']['media_results_queue_length'] == 0 and len(state['workers']) == 1, '主控/结果队列尚未回收')
    worker = state['workers'][0]
    require(worker['healthy'] is True and worker['restarts'] == 0 and worker['generation'] == 1
            and worker['stats']['active_calls'] == worker['stats']['processed_active_calls'] == 0
            and worker['stats']['available_blocks'] == 1, '媒体处理图或端口块未回收')
    sampled = utc(worker['admission']['sampled_at'])
    require(started <= minimum_sample <= sampled <= finished, '回收统计不是最后一次生命周期之后的新采样')


def evaluate_processed(receipt, base, current, as_of, verify_binaries=True):
    """M1 独立证据只能给一个明确原 ID 添加本地受限绿色，并支持发布包离线重放。"""
    proof = {'id': receipt.get('id', 'invalid') + ':' + PROCESSED_KEY, 'comparison_id': PROCESSED_KEY,
             'kind': PROCESSED_KIND, 'scope': PROCESSED_SCOPE, 'execution_id': receipt.get('id'),
             'test_names': [mode + ':' + name for mode in ('udp', 'connected') for name in PROCESSED_TESTS],
             'status': 'missing_evidence', 'reason': '缺少可重放的独立 M1 证据', 'executed': False, 'green_eligible': False,
             'started_at': receipt.get('started_at'), 'finished_at': receipt.get('finished_at')}
    try:
        artifacts = receipt['artifacts']
        def read(key): return read_artifact(base, artifacts[key])
        raw = json.loads(read('$receipt'))
        build = json.loads(read('$build'))
        require(raw.get('schema_version') == 1 and raw.get('kind') == PROCESSED_KIND
                and raw.get('binding_method') == 'source_before_and_after_actual_execution', '独立原始收据类型或执行绑定错误')
        require(build.get('schema_version') == '1.0.0' and build.get('kind') == 'local_runtime_build'
                and type(build.get('returncode')) is int and build['returncode'] == 0, '缺少当时成功的实际官方构建收据')
        require(raw['started_at'] == receipt['started_at'] and raw['finished_at'] == receipt['finished_at'], '导入器不能改写原执行时刻')
        started, finished = utc(raw['started_at']), utc(raw['finished_at'])
        require(utc(build['started_at']) <= utc(build['finished_at']) <= started <= finished <= as_of + timedelta(minutes=5)
                and (finished-started).total_seconds() <= 600, '构建/执行窗口超期或时刻不一致')
        same, contract = processed_source_binding(raw, build, current)
        proof.update(binding_contract=contract, source_snapshot_sha256=sha(wire(build['source_after'])))
        binaries = raw['binaries_before']
        require(binaries == raw['binaries_after'] and set(binaries) == {'control', 'media'}, '原始运行二进制发生改变')
        for role in ('control', 'media'):
            require(binaries[role] == receipt['binaries'][role]['sha256'] == build['binaries'][role]['sha256'], '运行/构建/归档二进制不一致')
            if verify_binaries: check_runtime_binary(base, receipt['binaries'][role])
        original_refs = processed_refs(raw)
        require(set(artifacts) == {'$receipt', '$build', *original_refs}, '原始引用被删减或混入未绑定证据')
        for key, reference in original_refs.items():
            require(sha(read(key)) == reference['sha256'], '原引用与导入文件 SHA 不一致')
        metadata = json.loads(read(raw['build_path']))
        require(metadata.get('ok') is True and metadata['source_before'] == metadata['source_after'], '原始构建记录失败或执行期间变化')
        for role, name in [('control', 'rustswitch'), ('media', 'rustswitch-media')]:
            item = metadata['binaries'][name]
            require((item['sha256'] if isinstance(item, dict) else item) == binaries[role], '原构建元数据与官方构建 SHA 不一致')
        require(raw.get('passed') is True and raw.get('outcome') == 'passed_scoped', '原始独立执行没有完整通过')
        require([stage['mode'] for stage in raw['stages']] == ['udp', 'connected'], '缺少两个独立 UDP 模式')
        connected_cases = cycles = catalog_cases = 0
        rejected_statuses = []
        all_artifact_paths = set()
        for stage in raw['stages']:
            require(type(stage.get('exit_code')) is int and stage['exit_code'] == 0
                    and stage.get('timed_out', False) is False and stage.get('forced_cleanup', False) is False
                    and stage.get('passed') == 11 and stage.get('failed') == stage.get('skipped') == 0
                    and 0 < stage['elapsed_seconds'] <= 120, '阶段失败、跳过、数量错误或超期')
            if 'started_at' in stage or 'finished_at' in stage:
                require(started <= utc(stage['started_at']) <= utc(stage['finished_at']) <= finished, '新阶段时刻超出实际运行窗口')
            require(stage.get('tests') == [{'id': name, 'status': 'ok'} for name in PROCESSED_TESTS], '必须运行固定的完整 11 项集合')
            log = read(stage['log']['path']).decode()
            require(not re.search(r'^FAILED|^(FAIL|ERROR):|\.\.\. (FAIL|ERROR|skipped)', log, re.M), '原始日志含失败或跳过标记')
            matches = re.findall(r'^test_\w+ \(__main__\.(ProcessedMediaIntegration\.test_\w+)\)\n[^\n]+ \.\.\. ok$', log, re.M)
            total = re.search(r'\nRan (\d+) tests in ([0-9.]+)s\n\nOK\n?\Z', log)
            require(matches == list(PROCESSED_TESTS) and total and int(total[1]) == 11
                    and float(total[2]) == stage['elapsed_seconds']
                    and len(re.findall(r'^test_\w+ \(', log, re.M)) == 11, '原始 unittest 输出缺项、被替换、失败或截断')
            require([case['test'] for case in stage['artifacts']] == ['__main__.' + name for name in PROCESSED_TESTS], '22 份用例原帧缺项或重复')
            for test, case in zip(PROCESSED_TESTS, stage['artifacts']):
                directory = Path(case['path']).parent.as_posix()
                expected_files = {directory + '/' + name for name in ('wire.json', 'config.json', 'events.jsonl', 'server.log')}
                require(set(case['artifacts']) == expected_files and not all_artifact_paths.intersection(expected_files), '用例文件清单缺失、混用或夹具被替换')
                all_artifact_paths.update(expected_files)
                for key, info in case['artifacts'].items(): require(len(read(key)) == info['bytes'], '原用例文件字节数不一致')
                document = json.loads(read(case['path']))
                require(len(document['wire']) == case['wire_records'], '原始包计数不一致')
                require(case.get('cleanup_confirmed_by_fresh_stats') is (test != PROCESSED_REJECT), '原始回收声明被修改')
                result = check_processed_wire(document, json.loads(read(directory + '/config.json')),
                                              read(directory + '/events.jsonl'), read(directory + '/server.log'),
                                              binaries, stage['mode'] == 'connected', test, started, finished)
                connected_cases += int(result['connected_case'])
                cycles += result.get('call_cycles', 0)
                catalog_cases += int(result.get('runtime_catalog_observed', False))
                if not result['connected_case']: rejected_statuses.append(result['rejection_status_recorded'])
        require(connected_cases == 20 and cycles == 22 and len(rejected_statuses) == 2, '真实生命周期和分配前拒绝覆盖不完整')
        require(contract != 'official_full_build_and_run_snapshots' or all(rejected_statuses), '新完整合同必须保存三个拒绝子场景的真实未分配统计')
        require(contract != 'official_full_build_and_run_snapshots' or catalog_cases == 2, '新完整合同缺少两种模式的实际实时 Codec 目录')
        proof.update(executed=True, log=artifacts[raw['stages'][0]['log']['path']], verified_binary_sha256=binaries,
                     connected_cases=connected_cases, call_cycles=cycles, rejection_cases=2,
                     runtime_catalog_observations=catalog_cases,
                     rejection_evidence='raw_488_and_recorded_zero_stats' if all(rejected_statuses) else 'raw_488_no_admission_and_bound_test_status_assertions')
        if not same or as_of >= finished + timedelta(days=LIFETIME_DAYS):
            proof.update(status='stale', reason='功能源码/测试/构建来源已变化或超过 30 天，必须完整重跑 M1')
        else:
            proof.update(status='passed', green_eligible=True, expires_at=(finished+timedelta(days=LIFETIME_DAYS)).isoformat(),
                         reason='完整 22 项、构建/运行源码与二进制、原 SIP/RTP 和新采样回收均通过严格限定门禁')
    except (OSError, UnicodeError, TypeError, KeyError, ValueError, AttributeError, OverflowError, zlib.error) as error:
        proof.update(status='failed', reason='M1 独立证据未满足门禁：' + str(error), green_eligible=False)
    return proof


def evaluate_capacity(receipt, base, current, as_of, verify_binaries=True):
    """核对5000路完整证据；即使通过也只给本地场景的附加声明，不改万路目标。"""
    status, reason = binding_status(receipt, current, as_of)
    result = {'id': receipt.get('id', 'invalid') + ':capacity-5000', 'comparison_id': 'key-media-capacity', 'kind': 'capacity_5000',
              'scope': '5000路本地双向媒体场景；仅对应此硬件/格式/时长，不是万路或FreeSWITCH成对容量认证',
              'execution_id': receipt.get('id'), 'test_names': ['callbench:5000-bidirectional-full-load'], 'status': status, 'reason': reason,
              'executed': False, 'source_snapshot_sha256': sha(wire(receipt.get('source_after', {}))),
              'started_at': receipt.get('started_at'), 'finished_at': receipt.get('finished_at'), 'green_eligible': False}
    try:
        artifacts = receipt['artifacts']
        report = json.loads(read_artifact(base, artifacts['report']))
        samples = json.loads(read_artifact(base, artifacts['status_samples']))
        cleanup = json.loads(read_artifact(base, artifacts['cleanup']))
        journal = read_artifact(base, artifacts['journal'])
        # 构建前清单必须来自真实构建收据，不能仅凭容量收据自报构建成功。
        build = json.loads(read_artifact(base, artifacts['build']))
        require(build.get('schema_version') == '1.0.0' and build.get('kind') == 'local_runtime_build'
                and type(build.get('returncode')) is int and build['returncode'] == 0, '缺少成功的实际容量构建收据')
        require(build.get('source_before') == build.get('source_after') == receipt['source_before'], '容量构建源与运行源不一致')
        require(utc(build['started_at']) == utc(receipt['started_at'])
                and utc(build['started_at']) <= utc(build['finished_at']) <= utc(receipt['finished_at']), '容量构建窗口不一致')
        for role in ['control', 'media', 'generator']:
            require(build['binaries'][role]['sha256'] == receipt['binaries'][role]['sha256'], '容量运行二进制与实际构建不匹配')
            if verify_binaries:
                check_runtime_binary(base, receipt['binaries'][role])
            else:
                require(re.fullmatch('[0-9a-f]{64}', receipt['binaries'][role]['sha256']), '缺少已归档二进制指纹')
        result['verified_binary_sha256'] = {role: receipt['binaries'][role]['sha256'] for role in ['control', 'media', 'generator']}
        result['log'] = artifacts['report']
        result['executed'] = True
        require(type(receipt.get('build_returncode')) is int and receipt['build_returncode'] == 0 and receipt.get('binding_method') == 'source_before_build_and_after_run', '容量二进制没有绑定实际构建')
        require(receipt.get('runtime_os') == 'linux' and isinstance(receipt.get('environment'), dict) and receipt['environment'], '缺少Linux实机环境描述')
        require(isinstance(report, dict) and report.get('passed') is True and report.get('media_server_bypassed') is False, '发生器没有通过真实媒体路径')
        require(report.get('requested_calls') == report.get('established_calls') == 5000, '没有建立5000路')
        seconds = report.get('media_seconds')
        require(isinstance(seconds, int) and not isinstance(seconds, bool) and seconds >= 10, '5000路媒体持续时间不足10秒')
        nominal, sent, got = 5000 * 100 * seconds, report.get('sent_packets'), report.get('received_unique_packets')
        require(isinstance(sent, int) and not isinstance(sent, bool) and sent > 0 and got == sent and sent >= .98 * nominal, '未提供完整双向负载或有缺包')
        require(report.get('nominal_packets') == nominal and abs(report.get('offered_load_ratio', 0) - sent / nominal) < 1e-9, '负载计数不一致')
        for key in ['unreceived_packets', 'duplicate_packets', 'invalid_packets', 'generator_write_errors', 'generator_reader_errors', 'underloaded_flow_directions', 'unexpected_received_packets', 'unknown_rtp_packets', 'unexpected_byes', 'teardown_failures']:
            require(type(report.get(key)) is int and report[key] == 0, '发生器缺包、错误或计数缺失')
        directions = report.get('directions', [])
        require(len(directions) == 2 and {d.get('source_side') for d in directions} == {0, 1}, '缺少两方向结果')
        require(all(d.get('sent', 0) > 0 and d.get('received') == d['sent'] and d.get('flows_with_loss') == 0
                    and d.get('minimum_received_per_flow', 0) >= .98 * 50 * seconds for d in directions), '逐方向或逐流没有提供至少98%名义负载并全部收回')
        require(sum(d['sent'] for d in directions) == sent and sum(d['received'] for d in directions) == got, '双向汇总不一致')
        require(all(cleanup.get(k) is True for k in ['complete', 'controller_exited', 'generator_exited', 'media_processes_exited', 'ports_released', 'work_dir_removed']), '未证实完整资源回收')
        require(isinstance(samples, list) and samples and any(s.get('active_calls') == 5000 and s.get('established_calls') == 5000 for s in samples), '缺少真实5000活跃/接通采样')
        require(all(isinstance(s.get('at'), str) and isinstance(s.get('workers'), list) for s in samples), '采样缺少时刻或逐分片状态')
        identity = receipt.get('controller_run_id')
        require(isinstance(identity, str) and identity and all(s.get('run_id') == identity for s in samples), '采样没有关联同一实际控制实例日志身份')
        times = [utc(s['at']) for s in samples]
        require(utc(build['finished_at']) <= times[0], '容量采样早于实际构建结束')
        require(utc(receipt['started_at']) <= times[0] and times == sorted(times) and times[-1] <= utc(receipt['finished_at']), '状态采样时刻无序或超出运行窗口')
        last = samples[-1]
        require(last.get('active_calls') == 0 and last.get('established_calls') == 0 and last['workers'], '未观察到状态归零')
        require(last.get('journal_healthy') is True and type(last.get('journal_lost_events')) is int and last['journal_lost_events'] == 0, '事件日志不健康或有丢失')
        for worker in last['workers']:
            stat = worker.get('stats', {})
            require(worker.get('healthy') is True and worker.get('restarts') == 0 and stat.get('active_calls') == 0, '分片未健康清空或曾重启')
            require(stat.get('socket_drop_counter_supported') is True, 'Linux缺少内核socket丢包可见性')
            require(all(type(stat.get(k)) is int and stat[k] == 0 for k in ['socket_rx_drops', 'send_errors', 'send_expired', 'send_queue_drops', 'invalid_packets', 'rate_limited', 'receive_errors']), '分片存在应用/内核丢包或计数缺失')
        # 日志独立证明同一启动身份下的并发峰值和最终回收，不把累计建立次数当同时活跃。
        active, peak, identities = set(), 0, set()
        for line in journal.decode().splitlines():
            event = json.loads(line)
            require(event.get('run_id') == identity, '事件日志混入其他实例或缺少身份')
            identities.add(event['run_id'])
            if event.get('kind') == 'call_established': active.add(event['call_id']); peak = max(peak, len(active))
            elif event.get('kind') == 'call_ended': active.discard(event['call_id'])
        require(len(identities) == 1 and peak == 5000 and not active, '事件日志不能证明同一实例5000并发及结束')
        result['measurement'] = {k: report[k] for k in ['requested_calls', 'media_seconds', 'sent_packets', 'received_unique_packets', 'unreceived_packets', 'offered_load_ratio']}
        result['measurement'].update({k: report.get(k) for k in ['payload_type', 'ptime_ms', 'codec', 'generator_os', 'generator_arch', 'measured_at']})
        result['environment'] = receipt['environment']
        result['controller_run_id'] = identity
        if status == 'passed':
            result['green_eligible'] = True
            result['expires_at'] = (utc(receipt['finished_at']) + timedelta(days=LIFETIME_DAYS)).isoformat()
    except (OSError, UnicodeError, TypeError, KeyError, ValueError, zlib.error) as error:
        result.update(status='failed' if result['executed'] else 'missing_evidence', reason='容量证据未满足完整门禁：' + str(error), green_eligible=False)
    return result


def historical_runs(history=HISTORY):
    """历史日志证明当时测试动作，不补造当时源码清单；不参与绿色判定。"""
    result = []
    path = history / 'go-race-delivery.jsonl'
    if path.is_file():
        try:
            trace = parse_go(path.read_bytes())
            result.append({'id': 'historical-go-race-delivery', 'status': 'failed' if trace['suite_failed'] or not trace['complete'] else 'unbound', 'green_eligible': False,
                           'log': {'path': path.name, 'sha256': sha(path.read_bytes())},
                           'test_pass_events': trace['test_pass_events'], 'package_pass': sorted(trace['package_pass']),
                           'test_names': sorted(p + ':' + t for p, t in trace['passed']),
                           'reason': '历史Go运行没有执行前后源码清单，不能用当前源码SHA补造绑定'})
        except (ValueError, UnicodeError, KeyError):
            result.append({'id': 'historical-go-race-delivery', 'status': 'missing_evidence', 'green_eligible': False})
    for name in ['codec-udp', 'codec-connected', 'sip-udp', 'sip-connected']:
        path = history / (name + '.log')
        if not path.is_file(): continue
        raw = path.read_text()
        cases = re.findall(r'^(test_\w+)\s+\(([^\n)]+)\)', raw, re.M)
        total = re.search(r'\nRan (\d+) tests? in ', raw)
        valid = total and int(total[1]) == len(cases) and raw.rstrip().endswith('OK') and raw.count('... ok') == len(cases)
        result.append({'id': 'historical-' + name, 'status': 'unbound' if valid else 'missing_evidence', 'green_eligible': False,
                       'log': {'path': path.name, 'sha256': sha(path.read_bytes())}, 'test_count': len(cases),
                       'test_names': [owner if owner.rsplit('.', 1)[-1] == case else owner + '.' + case for case, owner in cases],
                       'reason': '保留真实历史端到端结果；没有执行时源码绑定，不推导当前版本通过'})
    return result


def build(evidence_dir=DEFAULT_EVIDENCE, root=ROOT, history=HISTORY, as_of=None):
    """逐原ID建立独立运行视图；未映射条目全部未执行，原comparison状态原封不动。"""
    # 使用当前时刻处理日内到期；输出只记录日期，正常同日复查仍确定一致。
    as_of = as_of or datetime.now(timezone.utc)
    comparison_raw = (root / 'docs/api/comparison.json').read_bytes()
    comparison = json.loads(comparison_raw)
    identifiers = [r['id'] for r in comparison['entries']]
    require(len(identifiers) == len(set(identifiers)), '原对照ID重复')
    current, runtime = source_snapshot(root), source_snapshot(root, True)
    observations, receipts, integrity_errors, seen = [], [], [], set()
    input_hashes = {'docs/api/comparison.json': sha(comparison_raw)}
    for path in sorted(evidence_dir.glob('*.receipt.json')):
        try:
            receipt = json.loads(path.read_bytes())
            require(isinstance(receipt, dict) and receipt.get('schema_version') == '1.0.0'
                    and isinstance(receipt.get('id'), str) and re.fullmatch('[A-Za-z0-9_-]{1,128}', receipt['id']), '证据收据形状无效')
            require(receipt['id'] not in seen, '运行身份重复，不能覆盖或混用证据')
            seen.add(receipt['id'])
            receipts.append({'id': receipt['id'], 'kind': receipt.get('kind'), 'path': path.name,
                             'sha256': sha(wire(receipt)), 'record': receipt, 'embedded_artifacts': pack_evidence(receipt, path.parent)})
            if receipt.get('kind') == 'go_test_json':
                for identifier, claim in CLAIMS.items():
                    if identifier in identifiers:
                        observations.append(evaluate_go(receipt, path.parent, current, as_of, identifier, claim))
            elif receipt.get('kind') == 'python_unittest_e2e':
                for identifier, claim in E2E_CLAIMS.items():
                    if identifier in identifiers:
                        observations.append(evaluate_e2e(receipt, path.parent, runtime, as_of, identifier, claim))
            elif receipt.get('kind') == 'capacity_5000':
                observations.append(evaluate_capacity(receipt, path.parent, runtime, as_of))
            elif receipt.get('kind') == PROCESSED_KIND:
                if PROCESSED_KEY in identifiers:
                    observations.append(evaluate_processed(receipt, path.parent, runtime, as_of))
            elif receipt.get('kind') == pcm_verification.PCM_KIND:
                # 新的私有流接口逐项附限定证据，不扩大为原版 API 或 ASR 整链兼容。
                for identifier in pcm_verification.PCM_CLAIMS:
                    if identifier in identifiers:
                        observations.append(pcm_verification.evaluate_pcm(receipt, path.parent, runtime, as_of, identifier))
            elif receipt.get('kind') == rx_verification.RX_KIND:
                # 内部收音仅绑定真实三组四组合证据；不扩散为ASR供应商或原版媒体接口通过。
                for identifier in rx_verification.RX_CLAIMS:
                    if identifier in identifiers:
                        observations.append(rx_verification.evaluate_rx(receipt, path.parent, runtime, as_of, identifier))
            else: raise ValueError('未知运行证据类型')
        except (ValueError, OSError, UnicodeError, TypeError) as error:
            receipts.append({'id': path.stem, 'status': 'missing_evidence', 'reason': str(error), 'path': path.name})
            integrity_errors.append({'path': path.name, 'reason': str(error)})
    by_id = {}
    for observation in observations:
        by_id.setdefault(observation['comparison_id'], []).append(observation)
    entries = []
    for original in comparison['entries']:
        def execution_order(record):
            # 无效时刻不能被忽略后回退到较早成功；无法排序的证据优先呈现其失败状态。
            try:
                return utc(record.get('finished_at', '')).timestamp()
            except (ValueError, TypeError, AttributeError):
                return float('inf')
        candidates = sorted(by_id.get(original['id'], []), key=execution_order, reverse=True)
        # 每种声明先取最近一次；另一类子集通过不能遮盖当前已有的失败/失效声明。
        latest_by_kind = {}
        for candidate in candidates: latest_by_kind.setdefault(candidate['kind'], candidate)
        current_failures = [candidate for candidate in latest_by_kind.values() if candidate['status'] != 'passed']
        chosen = current_failures[0] if current_failures else candidates[0] if candidates else None
        status = 'missing_evidence' if integrity_errors else chosen['status'] if chosen else 'not_run'
        # 同一条目可以同时依赖Go和E2E；采用所有最新必需范围的最早期限，不能只看最后一份证明。
        effective_expiry = min((utc(o['expires_at']) for o in latest_by_kind.values()), default=None) if status == 'passed' else None
        entries.append({'comparison_id': original['id'], 'implementation_status': original['status'], 'local_status': status,
                        'green_eligible': bool(chosen and chosen['green_eligible'] and status == 'passed'),
                        'effective_expires_at': effective_expiry.isoformat() if effective_expiry else None,
                        'scope': chosen['scope'] if chosen else '没有绑定此原始条目的当前本地运行证据',
                        'reason': '运行收据集合损坏，须修复后重新复查' if integrity_errors else chosen['reason'] if chosen else '未采集绑定当前源码的必需运行证据',
                        'verification_id': chosen['id'] if chosen else None})
    result = {'version': '1.0.0', 'evaluated_on': as_of.date().isoformat(), 'comparison_sha256': sha(comparison_raw),
              'scope': '本地限定断言运行证据；不改写FreeSWITCH实现状态，不等价原版互通、万路或生产认证',
              'upstream_runtime_executed': False, 'paired_status': 'not_run', 'production_capacity_certified': False,
              'policy': {'green_requires': ['actual_test_pass', 'complete_required_tests', 'source_before_equals_after_equals_current', 'artifact_sha256_match', 'not_expired'],
                         'source_scope': '自研Go、测试、完整vendor、配置及XML模板；容量/E2E另含Rust/C。RX组合按其独立绑定合同核对完整Go/vendor、本地配置及Rust；前端和生成文档内容不在Go验证范围。',
                         'max_age_days': LIFETIME_DAYS, 'historical_unbound_is_green': False},
              'summary': {'comparison_entries': len(entries), 'by_local_status': dict(Counter(e['local_status'] for e in entries)),
                          'green_entries': sum(e['green_eligible'] for e in entries), 'observations': len(observations)},
              'source_snapshot': current, 'runtime_source_snapshot': runtime,
              'integrity_errors': integrity_errors,
              'receipts': receipts, 'observations': observations, 'historical_runs': historical_runs(history), 'entries': entries,
              'inputs': input_hashes}
    validate(result, comparison)
    return result


def validate(dataset, comparison):
    """防止漏项、状态偷换、无证据标绿及把本地验证升级成FreeSWITCH认证。"""
    original = {e['id']: e for e in comparison['entries']}
    entries = dataset['entries']
    require(len(entries) == len(original) and {e['comparison_id'] for e in entries} == set(original), '运行视图遗漏或重复原ID')
    require(dataset['upstream_runtime_executed'] is False and dataset['paired_status'] == 'not_run' and dataset['production_capacity_certified'] is False, '禁止升级原版/生产认证')
    observations = {o['id']: o for o in dataset['observations']}
    require(len(observations) == len(dataset['observations']), '运行声明ID重复')
    for entry in entries:
        require(entry['implementation_status'] == original[entry['comparison_id']]['status'], '不得修改原实现状态')
        require(entry['local_status'] in STATES, '未知本地运行状态')
        require((entry['local_status'] == 'passed') == entry['green_eligible'], '通过状态与绿色门禁不一致')
        proof = observations.get(entry.get('verification_id'))
        if entry['green_eligible']:
            require(not dataset.get('integrity_errors'), '损坏的运行收据集合不能标绿')
            require(entry['local_status'] == 'passed' and proof and proof['status'] == 'passed' and proof['green_eligible'] and proof['executed'], '缺少真实通过证据却标绿')
            require(proof['comparison_id'] == entry['comparison_id'] and proof.get('log') and proof.get('source_snapshot_sha256') and proof.get('expires_at'), '绿色证据绑定缺失')
            require(isinstance(entry.get('effective_expires_at'), str), '绿色条目缺少全部必需范围的最早期限')
    require(dataset['summary']['green_entries'] == sum(e['green_eligible'] for e in entries), '绿色计数失真')
    require(dataset['summary']['comparison_entries'] == len(entries)
            and dataset['summary']['by_local_status'] == dict(Counter(e['local_status'] for e in entries)), '运行状态统计失真')


def validate_runtime(root=ROOT, dataset=None, as_of=None):
    """发布器离线门禁：无需work目录，重验嵌入日志、当前源码、最近一次运行和有效期。

    二进制SHA来自采集时的实际文件核验；离线源码包不包含所有平台二进制，完整二进制仍应归档。
    """
    as_of = as_of or datetime.now(timezone.utc)
    dataset = dataset if dataset is not None else json.loads((root / 'docs/api/runtime-verification.json').read_bytes())
    comparison_raw = (root / 'docs/api/comparison.json').read_bytes()
    comparison = json.loads(comparison_raw)
    validate(dataset, comparison)
    require(dataset.get('version') == '1.0.0' and dataset.get('comparison_sha256') == sha(comparison_raw), '运行证据与当前对照不匹配')
    require(dataset.get('policy', {}).get('max_age_days') == LIFETIME_DAYS, '不得放宽证据有效期')
    receipts, rerun = {}, {}
    current, runtime = source_snapshot(root), source_snapshot(root, True)
    for item in dataset.get('receipts', []):
        record = item.get('record')
        if not record:
            require(not dataset['summary']['green_entries'], '存在损坏收据时不能发布绿色')
            continue
        require(item['id'] == record['id'] and item.get('sha256') == sha(wire(record)) and record['id'] not in receipts, '运行收据指纹或身份损坏')
        receipts[record['id']] = record
        for observation in dataset['observations']:
            if observation.get('execution_id') != record['id']:
                continue
            identifier = observation['comparison_id']
            if record['kind'] == 'go_test_json':
                require(identifier in CLAIMS, '未知Go断言映射不可标绿')
                proof = evaluate_go(record, item['embedded_artifacts'], current, as_of, identifier, CLAIMS[identifier])
            elif record['kind'] == 'capacity_5000':
                proof = evaluate_capacity(record, item['embedded_artifacts'], runtime, as_of, verify_binaries=False)
            elif record['kind'] == 'python_unittest_e2e':
                require(identifier in E2E_CLAIMS, '未知E2E断言映射不可标绿')
                proof = evaluate_e2e(record, item['embedded_artifacts'], runtime, as_of, identifier, E2E_CLAIMS[identifier], verify_binaries=False)
            elif record['kind'] == PROCESSED_KIND:
                require(identifier == PROCESSED_KEY, 'M1 处理图不能授予其他原版 API 或模块绿色')
                proof = evaluate_processed(record, item['embedded_artifacts'], runtime, as_of, verify_binaries=False)
            elif record['kind'] == pcm_verification.PCM_KIND:
                require(identifier in pcm_verification.PCM_CLAIMS, '私有 PCM 流不能授予其他原版 API 或模块绿色')
                proof = pcm_verification.evaluate_pcm(record, item['embedded_artifacts'], runtime, as_of, identifier, verify_binaries=False)
            elif record['kind'] == rx_verification.RX_KIND:
                require(identifier in rx_verification.RX_CLAIMS, '内部RX不能授予供应商、原版API或容量绿色')
                proof = rx_verification.evaluate_rx(record, item['embedded_artifacts'], runtime, as_of, identifier, verify_binaries=False)
            else:
                raise ValueError('未知收据类型')
            rerun[proof['id']] = proof
    for entry in dataset['entries']:
        if not entry['green_eligible']:
            continue
        proof = rerun.get(entry['verification_id'])
        stored = next(o for o in dataset['observations'] if o['id'] == entry['verification_id'])
        require(proof and proof['green_eligible'] and proof['status'] == 'passed', '绿色已失效：' + entry['comparison_id'])
        require(stored == proof and entry['scope'] == proof['scope'], '绿色证据或限定范围被修改')
        choices = [o for o in dataset['observations'] if o['comparison_id'] == entry['comparison_id']]
        latest = max(choices, key=lambda o: utc(o['finished_at']))
        require(latest['id'] == proof['id'], '较早通过不能遮盖最近失败')
        kinds = {o['kind'] for o in choices}
        latest_scopes = [max((o for o in choices if o['kind'] == kind), key=lambda o: utc(o['finished_at'])) for kind in kinds]
        current_scopes = [rerun.get(o['id']) for o in latest_scopes]
        require(all(o and o['status'] == 'passed' and o['green_eligible'] for o in current_scopes), '其他当前本地范围失败或失效，不能用子集通过遮盖')
        require(utc(entry['effective_expires_at']) == min(utc(o['expires_at']) for o in current_scopes), '绿色条目的最早复核期限不一致')
    return {'comparison_entries': len(dataset['entries']), 'green_entries': dataset['summary']['green_entries'],
            'offline_source_and_logs_checked': True, 'upstream_runtime_executed': False}


def markdown(dataset):
    """公开说明绿色的确切边界，历史通过只作为历史材料展示。"""
    lines = ['# 本地运行验证与绿色状态', '',
             '绿色表示所列本地断言或容量子场景真实通过，并且日志与执行前后源码指纹仍有效。原版FreeSWITCH成对结果由独立验收报告说明，不从本地测试推导。', '',
             f"本次覆盖 {len(dataset['entries'])} 个原始对照ID，当前可标绿 {dataset['summary']['green_entries']} 项。未映射、失败、源码变化、缺失或过期证据均不标绿。", '',
             '## 运行与复查', '',
             '```sh', 'python3 tools/build_runtime_verification.py --record-go --go /absolute/path/to/go',
             'python3 tools/build_runtime_verification.py --check', 'python3 -m unittest discover -s tools -p test_runtime_verification.py', '```', '',
             '`--record-go` 执行新的 `go test -race -count=1 -json -timeout=3m ./...`，记录执行前后源码、日志和诊断输出SHA。它不会给旧日志补造当时版本。更新源码后重新生成将显示过期；必须重新运行才能恢复绿色。默认证据目录为工作区 `work/audio-task/runtime-verification`，发布时应归档其中收据与日志。', '',
             '只绑定Go功能源码、测试、配置及XML模板；前端与生成文档内容不属于这些测试的通过范围，避免证据文档发布导致自身指纹递归变化。30天复核期限是本项目明确采用的证据新鲜度门禁，不是协议标准。', '',
             '发布器调用`validate_runtime(root)`：日志/报告以有界压缩内容随JSON发布，可在没有工作目录的源码包中重验日志SHA、run/pass及包完成、源码集合和过期门禁。二进制在采集时实际读文件核对SHA，源码包离线复查保留该指纹；核对二进制本体须使用完整运行证据包。运行收据不是第三方签名或独立认证。', '',
             '## 当前本地范围', '', '| 条目 | 本地状态 | 本次限定范围 |', '|---|---|---|']
    for e in dataset['entries']:
        if e['comparison_id'] in CLAIMS or e['comparison_id'] in E2E_CLAIMS or e['verification_id']:
            lines.append(f"| `{e['comparison_id']}` | {LABELS[e['local_status']]} | {e['scope']} |")
    lines += ['', '## 历史运行', '', '历史Go race及90个端到端场景保留真实测试名和日志SHA；没有执行时源码清单时统一标记未绑定，不把当时通过等同当前版本通过。', '',
              '## 新的SIP与媒体端到端采集', '',
              '```sh', 'python3 tools/build_runtime_verification.py --record-e2e --control /absolute/path/to/rustswitch --media /absolute/path/to/rustswitch-media --build-receipt /absolute/path/to/build.json', '```', '',
              '固定顺序执行SIP、codec、ESL、快照、应用生命周期、变量、放音控制、WAV、拨号计划、媒体交互和本地按键发送等十二套用例的UDP/connected二十四组完整用例，每组180秒上限；用例只操作自己创建的隔离进程。捕获实际run/pass集合、完整unittest日志、退出码、前后runtime源码和二进制SHA。出现失败、跳过、超时、子进程残留或用例仅列出均不绿。两种模式全部成功才授予对应的本地RTP/RTCP/DTMF/分片隔离/可信中继/SDP子集。', '',
              '构建收据build.json字段：schema_version="1.0.0"、kind="local_runtime_build"、started_at/finished_at、returncode=0、source_before/source_after（均由实际构建前后source_snapshot(root, True)采集）、binaries.control.sha256和binaries.media.sha256。先采集源码、实际构建、再采集源码与产物SHA；缺少构建收据时仍保留实际测试，但一律unbound。不得在构建完成后补造构建前清单。', '',
              '## Linux 5000路证据合同', '',
              '在证据目录保存 `*.receipt.json`，`schema_version=1.0.0`、`kind=capacity_5000`、唯一id、带时区started_at/finished_at、`binding_method=source_before_build_and_after_run`、`build_returncode=0`、完整source_before/source_after（使用本工具source_snapshot(root, True)）及runtime_os=linux/environment。', '',
              '`binaries.control/media/generator` 和 `artifacts.build/report/status_samples/cleanup/journal` 均为 `{path,sha256}`，路径必须在同一证据目录内。build是实际构建前后收据，来源清单与三个产物SHA必须相同且构建完成早于采样；report是原始callbench JSON；status_samples为扁平JSON数组，每项含at、run_id、active_calls、established_calls和完整workers；run_id由采集器从自己实例日志读取后关联，并非原status接口字段。收据controller_run_id与全部采样、事件一致。cleanup必须包含complete/controller_exited/generator_exited/media_processes_exited/ports_released/work_dir_removed六个真实true；journal为原始JSONL事件。', '',
              '门禁要求5000全部建立、至少10秒双向负载、名义包量至少98%、每方向minimum_received_per_flow达到98%×50×持续秒数且全部唯一包收到、generator_reader_errors及其他读写/重复/拆线错误为0、真实5000活跃采样、最终分片健康归零和journal_healthy且lost=0、Linuxsocket丢包计数可见且为零、日志并发峰值和完整回收。完整门禁未满足保留失败，不调阈值。', '',
              '即使5000路通过，只给`key-media-capacity`附加“5000路本地场景通过”标识；原来的万路目标仍未验证，不能修改其实现状态或作为FreeSWITCH成对性能结论。', '']
    return '\n'.join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence-dir', type=Path, default=DEFAULT_EVIDENCE)
    parser.add_argument('--record-go', action='store_true')
    parser.add_argument('--record-e2e', action='store_true')
    parser.add_argument('--import-processed', type=Path, help='导入已实际执行的独立 M1 收据，保留原始字节与实际构建快照')
    parser.add_argument('--import-pcm', type=Path, help='导入 SIP 授权 PCM 流实际收据，独立复核原帧与原始构建/运行指纹')
    parser.add_argument('--import-rx', type=Path, help='导入包含pool/pool_build/pause/server/media_build五份实际收据路径的JSON清单；只读重放，不执行清单代码')
    parser.add_argument('--go', type=Path)
    parser.add_argument('--python', type=Path, default=Path(sys.executable))
    parser.add_argument('--control', type=Path)
    parser.add_argument('--media', type=Path)
    parser.add_argument('--build-receipt', type=Path)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    require(sum([args.record_go, args.record_e2e, args.check, bool(args.import_processed), bool(args.import_pcm), bool(args.import_rx)]) <= 1, '每次只能选择一种实际采集、导入或check')
    exit_code = 0
    if args.record_go:
        require(args.go and args.go.is_file(), '--record-go需要真实Go可执行文件')
        _, exit_code = capture_go(args.evidence_dir.resolve(), args.go.resolve())
    elif args.record_e2e:
        require(args.control and args.control.is_file() and args.media and args.media.is_file(), 'record-e2e需要真实控制/媒体可执行文件')
        _, exit_code = capture_e2e(args.evidence_dir.resolve(), args.python.resolve(), args.control.resolve(), args.media.resolve(), args.build_receipt)
    elif args.import_processed:
        require(args.build_receipt and args.control and args.media, '导入 M1 需要当时官方构建收据与实际控制/媒体二进制')
        import_processed(args.import_processed.resolve(), args.build_receipt.resolve(), args.control.resolve(),
                         args.media.resolve(), args.evidence_dir.resolve())
    elif args.import_pcm:
        require(args.build_receipt and args.control and args.media, '导入 PCM 需要当时官方构建收据与实际控制/媒体二进制')
        pcm_verification.import_pcm(args.import_pcm.resolve(), args.build_receipt.resolve(), args.control.resolve(),
                   args.media.resolve(), args.evidence_dir.resolve())
    elif args.import_rx:
        manifest_path = args.import_rx.resolve()
        require(manifest_path.is_file() and manifest_path.stat().st_size <= 65536, 'RX收据路径清单缺失或超过预算')
        selected = json.loads(manifest_path.read_bytes())
        require(isinstance(selected, dict) and set(selected) == set(rx_verification.ROLES)
                and all(isinstance(value, str) and value for value in selected.values()), 'RX清单必须明确五份真实收据')
        rx_verification.import_rx({role: (manifest_path.parent / value).resolve() for role, value in selected.items()},
                                  args.evidence_dir.resolve())
    dataset = build(args.evidence_dir.resolve())
    validate_runtime(ROOT, dataset)
    outputs = {ROOT / 'docs/api/runtime-verification.json': wire(dataset), ROOT / 'docs/api/runtime-verification.md': markdown(dataset).encode()}
    for path, data in outputs.items():
        if args.check:
            require(path.is_file() and path.read_bytes() == data, '运行证据视图已过期，需要重新生成：' + path.name)
        else:
            path.write_bytes(data)
    print(json.dumps({'mode': 'checked' if args.check else 'generated', **dataset['summary'], 'record_exit_code': exit_code}, ensure_ascii=False))
    return 1 if exit_code else 0


if __name__ == '__main__':
    raise SystemExit(main())
