module rustswitch/control

go 1.23

// 公开单调时钟包装；固定版本及vendor保证Linux CGO=0与Darwin使用相同RXS2时钟域。
require golang.org/x/sys v0.30.0
