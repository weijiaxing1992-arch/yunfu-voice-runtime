// 本文件复现“总量足够但部分通话没有负载”的假通过反例，不把算例当作实际压力结果。
package main

import "testing"

// TestGlobalNinetyEightPercentCannotHideOneHundredIdleCalls 保留5000全建立、4900满载、100空载的反例。
func TestGlobalNinetyEightPercentCannotHideOneHundredIdleCalls(t *testing.T) {
	flows := make([]*flow, 5000)
	var sent, received uint64
	for i := range flows {
		f := &flow{index: i}
		f.accepted.Store(true)
		if i < 4900 {
			f.sent, f.received = [2]uint64{500, 500}, [2]uint64{500, 500}
		}
		flows[i] = f
		sent += f.sent[0] + f.sent[1]
		received += f.received[0] + f.received[1]
	}
	if sent != 4_900_000 || sent != received || float64(sent)/5_000_000 < 0.98 {
		t.Fatal("反例不再符合原有全局通过条件")
	}
	summary := assessFlowLoad(flows, 500)
	if summary.underloadedDirections != 200 || summary.missingPackets != 0 || summary.unexpectedReceived != 0 {
		t.Fatalf("全局98%%掩盖了100条空载通话: %+v", summary)
	}
}

// TestPerFlowThresholdAndCrossDirectionLoss 防止放宽98%门槛或用一个方向多收来抵消另一方向实际缺包。
func TestPerFlowThresholdAndCrossDirectionLoss(t *testing.T) {
	f := &flow{}
	f.accepted.Store(true)
	f.sent, f.received = [2]uint64{490, 490}, [2]uint64{490, 490}
	if summary := assessFlowLoad([]*flow{f}, 500); summary != (flowLoadSummary{}) {
		t.Fatalf("每方向精确98%%不应被额外降低或提高阈值: %+v", summary)
	}
	f.sent, f.received = [2]uint64{500, 500}, [2]uint64{501, 499}
	summary := assessFlowLoad([]*flow{f}, 500)
	if summary.missingPackets != 1 || summary.unexpectedReceived != 1 {
		t.Fatal("总接收数相同掩盖了实际方向缺包")
	}
	if sufficientDirectionLoad(489, 489, 500) || sufficientDirectionLoad(0, 0, 0) {
		t.Fatal("门槛不足或空负载被接受")
	}
}
