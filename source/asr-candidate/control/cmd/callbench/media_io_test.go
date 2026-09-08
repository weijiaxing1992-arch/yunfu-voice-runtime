// 本文件故障注入批量部分成功、短写和错误，不使用网络，也不忽略任何发送或接收失败。
package main

import (
	"errors"
	"io"
	"testing"
)

// batchResult 描述一次平台写入返回的前缀与错误，供故障场景逐步消费。
type batchResult struct {
	lengths []int
	err     error
}

// resultWriter 记录每次传入的首包，证明成功前缀不会在部分成功后再次提交。
type resultWriter struct {
	results []batchResult
	starts  []*flow
	count   uint64
}

// writeBatch 未指定结果时完整接受，指定结果时逐条模拟真实内核/截止时间行为。
func (w *resultWriter) writeBatch(packets []mediaDatagram) ([]int, error) {
	w.count++
	w.starts = append(w.starts, packets[0].flow)
	if len(w.results) > 0 {
		result := w.results[0]
		w.results = w.results[1:]
		return result.lengths, result.err
	}
	lengths := make([]int, len(packets))
	for i := range packets {
		lengths[i] = len(packets[i].data)
	}
	return lengths, nil
}

// calls 仅用于模拟计数，不能当作真实系统调用性能数据。
func (w *resultWriter) calls() uint64 { return w.count }

// TestBatchPartialSuccessNeverResendsPrefix 验证连续部分完成后每包只进入一次 sent。
func TestBatchPartialSuccessNeverResendsPrefix(t *testing.T) {
	packets := make([]mediaDatagram, 4)
	for i := range packets {
		packets[i] = mediaDatagram{flow: &flow{index: i}, data: make([]byte, 172)}
	}
	w := &resultWriter{results: []batchResult{{lengths: []int{172, 172}}, {lengths: []int{172}}}}
	transmitPackets(w, packets)
	if len(w.starts) != 3 || w.starts[0] != packets[0].flow || w.starts[1] != packets[2].flow || w.starts[2] != packets[3].flow {
		t.Fatal("部分批量成功后重复提交了成功前缀")
	}
	for _, packet := range packets {
		if packet.flow.sent[0] != 1 || packet.flow.writeErrors[0] != 0 {
			t.Fatal("成功前缀统计不是逐包准确值")
		}
	}
}

// TestBatchFailureAndShortWriteRemainFailures 防止超时、短写或零进展被误记成完整发包。
func TestBatchFailureAndShortWriteRemainFailures(t *testing.T) {
	for _, result := range []batchResult{
		{lengths: []int{172}, err: errors.New("write deadline")},
		{lengths: []int{171, 172}},
		{err: io.ErrShortWrite},
		{},
	} {
		packets := []mediaDatagram{{flow: &flow{}, data: make([]byte, 172)}, {flow: &flow{}, side: 1, data: make([]byte, 172)}}
		w := &resultWriter{results: []batchResult{result}}
		transmitPackets(w, packets)
		var sent, failed uint64
		for _, p := range packets {
			sent += p.flow.sent[p.side]
			failed += p.flow.writeErrors[p.side]
		}
		if failed == 0 || sent+failed != 2 {
			t.Fatalf("错误被隐藏或报文计数丢失: sent=%d failed=%d", sent, failed)
		}
		if w.count != 1 {
			t.Fatal("失败或零进展后无界重试")
		}
	}
}
