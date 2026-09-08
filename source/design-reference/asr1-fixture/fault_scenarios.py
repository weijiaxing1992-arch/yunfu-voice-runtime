"""下一批独立 ASR 模拟进程可加载的故障脚本；本文件只生成计划，不启动网络。

checkpoint 对应真实实现中的观测点。delay_ms 在本轮离线测试中只推进显式时钟，
下一批必须由实际进程暂停/有界闸门实现，不能用此计划文件本身充当实测结果。
"""
import json


SCENARIOS = {
    'ready_timeout': {'checkpoint': 'provider.before_ready', 'action': 'pause', 'delay_ms': 2100,
                      'expect': 'startup_timeout; RX 未订阅; 连接和额度归零'},
    'partial_stream': {'checkpoint': 'provider.decoded_count', 'action': 'emit_partial_every', 'frames': 2,
                       'expect': 'FINISH 前已经收到与真实 PCM 摘要一致的 partial'},
    'sdk_expired': {'checkpoint': 'adapter.after_rx_read', 'action': 'pause', 'delay_ms': 120,
                    'expect': '原 expires 不变; AUDIO 提交数为零; expired_before_submission'},
    'resample_expired': {'checkpoint': 'adapter.after_resample', 'action': 'pause', 'delay_ms': 120,
                         'expect': '16k 字节不重锚期限; 拒绝发送'},
    'write_lock_expired': {'checkpoint': 'adapter.after_write_permission', 'action': 'pause', 'delay_ms': 120,
                           'expect': '实际 Write 前再次拒绝过期帧'},
    'partial_write_expired': {'checkpoint': 'adapter.after_partial_write', 'action': 'limit_then_pause',
                             'accepted_bytes': 17, 'delay_ms': 120,
                             'expect': 'submission_unknown; 关闭连接; 不接续下一帧、不重放'},
    'provider_consumption_expired': {'checkpoint': 'provider.after_complete_audio_read', 'action': 'pause', 'delay_ms': 120,
                                    'expect': '内核/应用已收不等于已消费; provider 队列计数不增加'},
    'slow_reader': {'checkpoint': 'provider.before_read', 'action': 'pause', 'delay_ms': 150,
                    'expect': '单流超时; 其他流/媒体/DTMF/TX 保持进展'},
    'late_final_after_finish': {'checkpoint': 'provider.before_final', 'action': 'pause', 'delay_ms': 150,
                               'expect': '正常 Finish 且通话仍活跃时允许 final; 不给文本套 PCM 期限'},
    'late_final_after_hangup': {'checkpoint': 'provider.before_final', 'action': 'wait_for_signal', 'signal': 'actual_sip_bye_200',
                               'expect': '真实 BYE 撤销后不发布 final; stale_result 增加'},
    'late_old_segment': {'checkpoint': 'provider.before_partial', 'action': 'hold_until', 'signal': 'next_source_boundary',
                         'expect': 'partial_invalidated 后旧段转录不发布; 新段可以继续'},
    'close_before_done': {'checkpoint': 'provider.after_final', 'action': 'close',
                          'expect': 'unexpected EOF; final 不是流完成'},
    'wrong_done_prefix': {'checkpoint': 'provider.before_done', 'action': 'increment_field', 'field': 'samples',
                          'expect': '拒绝 DONE; 不归为 completed'},
    'duplicate_final': {'checkpoint': 'provider.after_final', 'action': 'repeat_with_next_result_seq',
                        'expect': '拒绝同 utterance 第二次 final'},
    'wrong_token': {'checkpoint': 'provider.before_result', 'action': 'replace_token',
                    'expect': '跨连接结果拒绝'},
    'result_overflow': {'checkpoint': 'adapter.before_next', 'action': 'hold_consumer', 'finals': 9,
                        'expect': '第九项明确 result_overflow; 不悄悄丢 final'},
    'half_frame_eof': {'checkpoint': 'provider.before_result', 'action': 'send_prefix_then_close', 'bytes': 17,
                       'expect': 'truncated_eof; 不当正常结束'},
    'oversize_length': {'checkpoint': 'provider.before_result', 'action': 'replace_length', 'length': 8193,
                        'expect': '读正文前拒绝; 缓冲不增长超过上限'},
}


def chunks(wire, sizes):
    """只切分已经给定的有限字节串，保留所有字节和原期限，不模拟成功写入。"""
    if any(type(size) is not int or size <= 0 for size in sizes):
        raise ValueError('分片尺寸必须为正整数')
    result, offset = [], 0
    for size in sizes:
        result.append(wire[offset:offset + size])
        offset += size
        if offset >= len(wire):
            break
    if offset < len(wire):
        result.append(wire[offset:])
    return [item for item in result if item]


if __name__ == '__main__':
    # 输出供评审和未来真实 mock 加载；不执行 delay、不创建进程或监听器。
    print(json.dumps({'schema': 'asr1-fault-plan-v1', 'network_executed': False,
                      'scenarios': SCENARIOS}, ensure_ascii=False, indent=2))
