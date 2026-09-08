# FreeSWITCH 逐项验证账本

**目前不能无感替换FreeSWITCH，也没有全面超越或生产万路媒体认证。**

账本保留当前对照全部 3999 个原始ID，另关联145项模块并集与35个业务域。原版运行环境未采集、动态差分执行0次。

完整记录：[JSON](verification-ledger.json)；逐项便览：[CSV](verification-ledger.csv)。

## 各层证据分别表示什么

- 固定来源：文件哈希与声明行号可核验；头文件、注册宏或配置出现位置不是完整运行合同。
- 本地源码：定位实际入口或缺失边界；配置文件存在不表示对应模块执行。
- 本地测试：仅列真实用例定义及明确关联，未执行的用例保持未跑。通用XML回归不会变成875项参数业务通过。
- 原版运行：环境和轨迹未知，executed=false、result=unknown。
- 成对验收：status=not_run、passed=null；没有默许的归一化白名单，也没有兼容成功率。

## 当前实现状态分布

| 原对照状态 | 数量 | 含义 |
| --- | ---: | --- |
| `export_only` | 882 | 配置产物 |
| `implemented` | 19 | 自有接口已实现 |
| `internal_only` | 23 | 内部合同 |
| `not_implemented` | 2844 | 兼容入口未实现 |
| `not_verified` | 178 | 验收未执行 |
| `partial` | 53 | 部分相关能力 |

静态枚举727个本地用例定义，23条对照有人工指定的直接本地回归定义；这些数字都不是通过数。

## 多编码更新边界

多编码的控制协商、测试向量、Rust worker实际接受、原生编解码自检、跨编码通话是五项独立证据。当前源树已扩展明确编码身份与RTP时钟，旧G.711-only措辞不再准确；不能从格式列表推导原模块兼容或转码通过。

当前编码/格式来自控制协商与测试配置。G722音频采样率和RTP时钟分开、G726与AAL2打包分开、动态PT按rtpmap识别。音频自检、同编码字节往返、重新分包、跨编码转换及原FreeSWITCH codec模块宿主必须分别交付证据；不能把新增原生源码或构建脚本直接当作通话路径已完成。

## 原始分母和新增范围

账本的entries逐个保留comparison.json原ID、状态和原条目SHA256。新增关键对照会自动进入分母；原版注册、事件、变量、原生函数和参数清单不因自有扩展而减少。
`mod_com_g729`仅完整树构建目录、`sdk/autotools`开发示例分别保留在145项模块审计中；未在原comparison出现的模块不会伪造一个原始记录。第三方、商业与现场自行构建模块仍不在已冻结现场清单中。

## 复验方式

在项目根目录先生成最终对照，再生成账本；源码或输入变化后旧账本的check会失败。

```sh
python3 tools/build_feature_audit.py
python3 tools/build_interface_comparison.py
python3 tools/build_verification_ledger.py --self-test
python3 tools/build_verification_ledger.py --check --self-test
```

另可执行 `python3 tools/test_verification_ledger.py`，覆盖来源/测试孤立引用、业务域漏项及配置通用回归不传播为业务通过。

--check只读比对原ID一一覆盖、原条目指纹、固定源哈希、当前源码/用例行号、模块与业务域分母、反虚假通过门禁及产物字节。生成器不编译、不启动服务、不发媒体、不运行原版，不会把其他任务的口头测试结果自动录为本账本证据。

下一步须冻结双方二进制、配置、依赖与客户端，为每个启用接口展开强制正常、边界、错误、并发、重试和副作用断言。保留原始报文/事件偏序/文件/外部请求，再分别出usage兼容、万路性能、drain和live迁移结论。
