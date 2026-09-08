# 压测 Go 运行时诊断：日志观察与边界

`tools/analyze_testlab_runtime.py` 是只读诊断工具，分析已经结束的测试报告或明确指定的 Go 进程日志。它不启动压力、重启服务、修改配置或覆盖旧测试结果，也不会把调度信息解释为媒体通过。

## 使用方式

```sh
python3 tools/analyze_testlab_runtime.py \
  --report /absolute/path/test-report.json \
  --output /absolute/path/new-runtime-observation.json
```

默认分别读取报告的 `logs.controller_tail` 和 `logs.generator_tail`。可使用 `--controller-log`、`--generator-log` 指定相应进程的完整日志。输出使用独占创建，已有文件报错，避免覆盖失败证据。输入上限 32MiB，单行上限 8192 字符，每角色最多保留 2048 条解析事件；超出部分明确计数。

需要采集新证据时，只在已批准的隔离测试进程上设置 `GODEBUG=gctrace=1,schedtrace=100,scheddetail=0`，并保存测试配置、进程身份、启动时刻和原日志。不要把构建时多个 Go 工具进程的输出拼成一个运行实例的轨迹，也不要在默认主服务中开启逐包诊断。开启诊断可能影响被测进程，结论必须记录该条件。

## 解析结果的含义

GC 的三个 wall-clock 阶段分别保存为清扫停止时间、并发标记时间和标记结束停止时间。并发标记时间不属于 STW；单次最大停止时间和两个停止段之和分别报告，不能把整个 GC 历时当作全程序暂停。

SCHED 行读取实际 `gomaxprocs/idleprocs/threads/spinningthreads/needspinning/idlethreads/runqueue`、各 P 队列以及可选的 `schedticks`。后者是另一个字段，不能当成额外运行队列。未知行、非法数值、截断、重复字段和时钟回退均有诊断，不猜测缺失数据。

两个时间轴分别标为 `runtime_start` 和 `first_schedtrace`；控制器与发生器也相互独立。没有同步证据就不能直接将某条 GC 行与某个 RTP 迟发时刻对齐。实际 `GODEBUG` 格式只证明可观察运行时事件，不证明当前媒体问题一定来自 GC、线程数或操作系统。

| 情况 | 输出解释 |
| --- | --- |
| 没有对应 trace | `not_observed`，最大 STW/线程数为 null，不补 0 |
| 只有日志尾部 | 明确有限窗口，不推断整段测试没有异常 |
| 有 GC/SCHED | 只报告实际解析事件与时间域，不自动给出原因 |
| 原媒体报告失败 | `test_passed` 保持 false，诊断不改变验收结论 |

完整覆盖默认不成立。要定位共同迟发，还需结合每个进程的真实时间映射、UDP 写入边界、worker 统计和有限媒体捕获；事件同时出现只能作为关联线索。

## 本轮验证

解析器 11 个测试已通过，覆盖真实格式、双进程分离、缺失轨迹、错误格式、有界保留及拒绝覆盖。另用当前实际 Go 工具链编译并执行小型 GC/SCHED 夹具，校验 4 条 GC 和 5 条 SCHED、未知格式为 0。这是解析验证，不是 RTP 压力或产品性能证明。

原 500 路失败报告的两个日志角色都没有这类 trace，实际分析结果为 `not_observed`。旧失败保持，不能据此声明没有 GC/调度暂停，也不能将失败归因于 GC。原文件及校验记录位于工作目录 `work/voice-runtime-m2-local/review/`，其中首轮未识别 schedticks 的观察亦保留。
