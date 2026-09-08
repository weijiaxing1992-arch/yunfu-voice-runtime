package media

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"rustswitch/control/internal/config"
	"testing"
)

// TestPoolExcludedBlocksStayInTheirShard 保证排除项按原有四端口分片透传，不改变相邻 worker 的端口边界。
func TestPoolExcludedBlocksStayInTheirShard(t *testing.T) {
	t.Setenv("RUSTSWITCH_POOL_MEDIA_HELPER", "normal")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(context.Background(), config.Config{Media: config.Media{Binary: executable, Workers: 2, PortStart: 20000, PortEnd: 20039, ExcludedPortBlocks: []int{20000, 20016, 20020, 20036}}, Limits: config.Limits{MaxCalls: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for index, expected := range [][]int{{20000, 20016}, {20020, 20036}} {
		cfg := p.Workers[index].Config
		if cfg.PortStart != 20000+index*20 || cfg.PortEnd != 20019+index*20 || !reflect.DeepEqual(cfg.ExcludedPortBlocks, expected) {
			t.Fatalf("错误的分片边界或排除项：%+v", cfg)
		}
		wire, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(wire, &fields); err != nil {
			t.Fatal(err)
		}
		var actual []int
		if err = json.Unmarshal(fields["excluded_port_blocks"], &actual); err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatal(string(wire), err)
		}
	}
}

// TestCodecRequestPreservesIndependentClocks 验证 JSON 没有把采样率与 RTP/电话事件时钟合并，null 仍能取消辅助载荷。
func TestCodecRequestPreservesIndependentClocks(t *testing.T) {
	pt := uint8(101)
	q := Request{Op: "allocate", Payload: 9, Codec: &CodecSpec{Name: "G722", SampleRate: 16000, RTPClockRate: 8000, Channels: 1, PTimeMS: 20}, DTMFPayload: &pt, DTMFClockRate: 8000, DTMFEvents: "0-15"}
	wire, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var round Request
	if err = json.Unmarshal(wire, &round); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(q, round) {
		t.Fatalf("JSON 改变协商参数：%s", wire)
	}
	legacy, err := json.Marshal(Request{Op: "connect", Payload: 8})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(legacy, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["codec"]; ok || string(fields["dtmf_payload"]) != "null" || string(fields["cn_payload"]) != "null" {
		t.Fatalf("旧命令默认语义改变：%s", legacy)
	}
}
