# 受限 FreeSWITCH XML 拨号计划与基础应用

文档版本 1.7.0。此入口实际驱动本地 SIP 呼入、Rust 媒体与有序应用；兼容范围是下面明确列出的 XML 子集，不是完整 `mod_dialplan_xml`、全部 FreeSWITCH 应用或任意部署配置兼容。

## 启用与载入时机

在完整启动配置中增加：

```json
{
  "sip": {
    "dialplan": {
      "file": "/etc/rustswitch/dialplan.xml",
      "context": "default"
    }
  },
  "media": {
    "playback_root": "/var/lib/rustswitch/prompts"
  }
}
```

这是字段片段，不能直接作为完整 `PUT /v1/config` 正文。`sip.dialplan` 省略时保持既有 `local_extensions`／固定上游路由；启用时它是当前来话的权威本地路由，号码未命中返回 404，不再退回 `local_extensions` 或固定上游。`context` 省略或空值使用 `default`；指定 context 必须实际存在，否则启动失败。context 最多 64 字节，不支持动态展开、空白分段或路径。

`file` 必须是绝对路径，最长 4096 字节，目标须为普通文件，最终软链接拒绝；完整内容最多 256 KiB。进程在启动监听与媒体子进程之前读取并严格校验一次，之后按不可变快照运行。磁盘改动不会热加载。XML文件、context和放音根目录由部署启动文件管理，后台只读；修改部署后正常重启重新载入，完整PUT须原样保留这些字段；`reloadxml` 和动态 XML Curl 尚未接入。管理页面现有 XML 草稿依然是独立导出产物，不能据保存成功认定本计划已经执行。

文件提示音必须配置 `media.playback_root`；只保存目录路径，不自动创建或上传文件。XML 内存在文件播放动作而未配置 root 时启动失败；具体文件缺失或损坏在实际加载时返回失败。文件系统、格式、缓存与线程预算见 [媒体交互接口](media-interaction.md)。

## 可直接使用的 XML 示例

```xml
<include>
  <context name="default">
    <extension name="local-menu">
      <condition field="destination_number" expression="^7101$">
        <action application="answer"/>
        <action application="set" data="customer=中文客户"/>
        <action application="playback" data="welcome.wav"/>
        <action application="read" data="1 4 silence digits 3000 # 1000"/>
        <action application="sleep" data="200"/>
        <action application="hangup" data="NORMAL_CLEARING"/>
      </condition>
    </extension>
  </context>
</include>
```

支持三种完整单文件包装：上例的 `<include><context .../></include>`、单独 `<context .../>`，以及 `<document type="freeswitch/xml"><section name="dialplan"><context .../></section></document>`。`include` 在这里是容器名称，不是文件包含指令。document 只允许一个 dialplan section，不读取其他配置 section。

每个 extension 恰好一个 `condition`，只允许 `field="destination_number"` 和 `expression`。同一 context 按声明顺序选择第一个匹配 extension，动作按 XML 声明顺序执行。表达式使用 Go RE2，每个最多 512 字节；不支持 PCRE 的反向引用、前后查找等扩展，也不展开 `$1`、`${...}` 或 `$${...}`。想要精确匹配请明确使用 `^` 和 `$`，未加锚点遵循普通正则子串匹配。

| 边界 | 当前上限 |
| --- | --- |
| XML 字节数 | 256 KiB |
| XML 元素／深度 | 4096 个／8 层 |
| 单元素属性 | 8 个；只接受对应节点已列出的属性 |
| context／extension 总量 | 16／256 |
| 同一 context 的 extension 名 | 唯一，最多 64 字节 |
| 每个 extension 动作／整文件动作 | 64／1024 |
| 单个 action data | 4096 字节，不含 CR、LF、NUL 或变量展开 |
| destination_number 匹配输入 | 最多 64 字节 |

未知节点、属性、应用和不支持的参数会使整份文件载入失败，即使它们位于当前号码不会命中的 extension。DTD、外部实体、处理指令、预处理、通配 include、嵌套 condition、多个 condition、anti-action、`inline`、`continue`、`break` 属性、任意字段条件和捕获替换都未实现，不会静默跳过。

## 真实运行顺序

每个 extension 首个动作必须是无参数 `answer`。命中时只创建本地 A 腿，分配真实媒体；分配成功才开始 `answer` 并发出带媒体地址的 SIP 200。匹配 ACK 到达后，`answer` 完成并继续下一条动作。这个 ACK 完成时点是明确的候选合同，不能推广为原版所有 answer 事件时序都一致。缺少 ACK、CANCEL、通话期限和媒体故障仍沿实际 SIP 状态机清理。

后续应用共用 [ESL 应用执行器](ivr-reference.md)，即使 ESL 监听关闭，XML 也可以实际收号、等待和挂机。未配置 ESL 只是不对外发布该套接字事件，不影响内部通道身份与应用推进。

动作表启动时只读共享，每通话保留位置并逐条受理，不为每通话复制所有 XML 或创建线程。运行中应用失败会终止计划并发起真实拆线，不会继续后面的动作；普通 `read` 超时仍通过 `read_result` 区分结果，本版本不具备基于其变量再次计算 XML 条件的菜单跳转。全部动作执行完后发起正常拆线；需要保持通道时应以 `park` 作为末尾动作。

## 基础应用

| 应用 | 参数与实际行为 | 当前限制 |
| --- | --- | --- |
| `answer` | 无参数；XML 首次执行真实发送 200 并等待匹配 ACK；已建立通道再次调用完成为空操作 | 不接受 answer flags；未接通的上游桥接腿不能被此应用虚假接通 |
| `set`／`unset` | 写入／删除当前腿变量，完成事件可观察实际值 | 128 项、64 KiB 变量预算；不包含全部原版变量副作用 |
| `sleep` | 十进制整数 0–3600000 毫秒；0 立即完成，非零到实际期限再推进下一应用 | 不接受空值、负数、单位后缀；不实现 `sleep_eat_digits` 等副作用 |
| `park` | 无参数；保持当前应用直至真实挂机、故障或通话最大时长结束 | 不以 60 秒伪造完成；未实现原版 park 内部处理私有执行事件、抢占或恢复机制 |
| `hangup` | 无参数或 `NORMAL_CLEARING`；实际发起 SIP BYE／取消并进入媒体释放 | 不支持全部 cause 映射；应用结束不等于对端已回复 BYE 或媒体释放已确认 |
| `read` | 既有有界收号语法，提示可用 `silence`、受限 tone 或相对 WAV | 详见 [IVR 读号参数](ivr-reference.md)，仍无完整 DTMF 缓存和任意正则菜单 |
| `playback` | 受限单频 tone 或 root 内相对 WAV，等待实际媒体完成 | 不支持 URL、文件列表、TTS、任意 seek／循环、视频或实时转码 |

ESL 调用这些应用仍使用 `sendmsg <UUID>`、`call-command: execute` 与 `event-uuid`。受理 `+OK` 不能当成完成。忙时需要 `event-lock: true` 才能进入原有每腿八项的串行队列；排在 park 后的任务不会自动抢占 park。终止通道可使用已实现的 `uuid_kill`，真实挂机会取消尚未开始的任务并释放额度。

`sleep` 用单个可替换计时器，park 不增加周期轮询任务；二者不受读号／提示音的 60 秒应用硬期限误判。整个通话仍受 `sip.max_call_seconds` 限制，因此一小时 sleep 不能绕过更短的通话上限。任务总量仍为 `min(max(2 × max_calls + 128, 128), 20000)`，另有 16 MiB 记账预算。XML 受理也计入这些限制。

## 文件播放与收号提示

`playback welcome.wav`、`playback menus/welcome.wav` 都相对于 `media.playback_root`。路径最多 240 UTF-8 字节、八个组件，每组件最多 128 字节；拒绝绝对路径、父目录、反斜线、URL、控制字符和非规范路径。`.wav` 后缀忽略大小写。单独 playback 支持普通空格文件名；`read` 参数按空白分词，当前不能引用带空格的提示音路径。

文件格式限 RIFF/WAVE 的 PCM16、PCMA 或 PCMU，均须 8 kHz 单声道；文件最大 1 MiB、音频最长 30 秒。实际播放到当前 PCMU／PCMA 8 kHz 媒体腿；这些文件格式能力不等于实时多编码转换或任意声道／采样率重采样。

媒体返回 `loading` 时文件尚未准备好，发送包数和总包数都为 0；必须继续等待 `running`、`completed`、`stopped` 或 `failed`。自然结束时须全部目标包被本机发送接口接受并确认completed；主动uuid_break或终止键打断则等待真实stopped。两类结束按原版均返回 `FILE PLAYED`，不能凭该文本推断文件完整播放。文件损坏、缺失、队列饱和与媒体异常均不能变成播放成功。实际远端是否收到并播放仍需端到端证据。

`read 1 4 prompt.wav digits 3000 #` 在文件提示音期间可以接收真实按键，申请停止媒体并等待停止确认后继续收号或完成。加载中的取消也必须关联实际 playback ID，迟到加载结果不能恢复已停止或已挂断的通话。挂断统一释放会话媒体，后续加载确认不会命中新的电话。

## 验证与未兼容边界

`tests/dialplan_e2e.py` 提供真实 SIP 与 Rust 媒体用例：XML 顺序／sleep／park、404 与重传、错误／重复 ACK 和迟到 CANCEL、缺 ACK 回收、关闭 ESL 时 RTP 收号并挂机、WAV 实际媒体及后续拆线、无效 WAV 阻断后续动作、文件提示音按键打断。测试分别支持普通和 connected UDP；通过只证明这些场景。

独立解析器和控制层测试验证未知 XML／应用拒绝、文件与表达式预算、真实顺序、长 sleep 不在 60 秒提前完成、park 无轮询及挂断资源清理。原版脚本另保存实际报文与输出差异；本地单测、加载成功、原版入口存在都不等于 FreeSWITCH 全功能兼容认证。

尚未提供完整 XML 拨号计划求值、Lua／JavaScript、bridge／originate／transfer、动态 context、反向动作、多条件、正则捕获展开、全部 application flags／channel variables、原版 park 重入语义、全部挂机 cause、文件定位／循环、录音、会议、ASR／TTS 和跨进程会话恢复。详见 [完整对照表](freeswitch-comparison.md) 与 [成对验证规则](conformance-testing.md)。

## 播放终止与原版响应（1.7）

`playback_terminators` 缺省为 `*`；`none`禁用，`any`匹配0–9、*、#，或设置文档化的字面菜单按键集合。每次播放先清除旧 `playback_terminator_used`，实际终止按键写入该变量；API `uuid_break <uuid>`没有按键，不伪造该变量。

普通uuid_break受理返回+OK，当前playback需等待真实停止再以FILE PLAYED完成。read中的uuid_break仅停止仍在播放的提示音，继续按读号条件与期限等待；不是立即成功收号。all/both、任意应用打断和park抢占未实现并明确拒绝。

缺失WAV的完成正文为FILE NOT FOUND；损坏/不支持格式仍明确错误。XML在加载失败后停止计划并挂机，不将FILE NOT FOUND误认成可继续的成功。FILE PLAYED同时覆盖原版的自然结束和主动中断，不能代替完整音频包数检查。
