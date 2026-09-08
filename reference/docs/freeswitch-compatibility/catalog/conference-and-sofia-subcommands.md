# Conference与Sofia子命令源码目录

固定参考：FreeSWITCH v1.11.3，commit `ef32e205295e29f034f1453ad245ba5efb07b94a`。全部项目均为**未运行验证**。本目录只补充两条常用命令树，不声称穷尽全部模块。

已解析conference_api_sub_commands[]全部 **84 条声明**，另列 **6 条全局入口分支**。Sofia列出 **38 条已核对解析分支候选**及 **2 条仅帮助文本候选**。JSON保留处理器、分发方式、源文件行和SHA-256。

## 使用边界

- 参数表达式记录静态元数据或源码入口结构；默认值、响应字节、事件、时序、权限和实际模块集合均须运行验证。
- 作用域是影响范围提示，不是逐房间或逐profile授权的实现证明。CLI、ESL、HTTP、Verto入口需分别认证，不能据此自动开放REST端点。
- conference分发名与帮助显示名分别保存。例如pause_play的帮助名称为pause，而pause自身指向录音暂停处理器。

## Conference：完整静态命令表

前缀为`conference <conference_name>`。原始语法拼写和括号保留，不在目录里修复。该表完整仅指这张静态表的声明，不代表处理器内部子参数全部展开。

| 分发子命令 | 参数表达式 | 中文用途 | 分发提示 | 来源 |
|---|---|---|---|---|
| `canvas-auto-clear` | `<canvas_id> <true\|false>` | 控制画布自动清理 | 分词参数 | [L46](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L46) |
| `count` | 空字符串 | 读取会议、成员或媒体状态 | 分词参数 | [L47](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L47) |
| `list` | `[delim <string>]\|[count]` | 读取会议、成员或媒体状态 | 分词参数 | [L48](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L48) |
| `xml_list` | 空字符串 | 读取会议、成员或媒体状态 | 分词参数 | [L49](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L49) |
| `json_list` | `[compact]` | 读取会议、成员或媒体状态 | 分词参数 | [L50](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L50) |
| `energy` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L51](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L51) |
| `auto-energy` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L52](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L52) |
| `max-energy` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L53](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L53) |
| `agc` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L54](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L54) |
| `vid-canvas` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 控制成员视频或会议视频布局 | 成员目标 | [L55](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L55) |
| `vid-watching-canvas` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 控制成员视频或会议视频布局 | 成员目标 | [L56](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L56) |
| `vid-layer` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 控制成员视频或会议视频布局 | 成员目标 | [L57](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L57) |
| `volume_in` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L58](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L58) |
| `volume_out` | `<member_id\|all\|last\|non_moderator> [<newval>]` | 调节成员音频阈值、增益或音量 | 成员目标 | [L59](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L59) |
| `position` | `<member_id> <x>:<y>:<z>` | 控制成员空间、听说关系或发言选择 | 成员目标 | [L60](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L60) |
| `auto-3d-position` | `[on\|off]` | 控制成员空间、听说关系或发言选择 | 分词参数 | [L61](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L61) |
| `play` | `<file_path> [async\|<member_id> [nomux]]` | 控制文件播放、等待音乐或语音输出 | 分词参数 | [L62](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L62) |
| `moh` | `<file_path>\|toggle\|[on\|off]` | 控制文件播放、等待音乐或语音输出 | 分词参数 | [L63](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L63) |
| `pause_play` | `[<member_id>]` | 暂停或恢复文件播放 | 分词参数；帮助名=pause | [L64](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L64) |
| `play_status` | `[<member_id>]` | 读取会议、成员或媒体状态 | 分词参数 | [L65](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L65) |
| `file_seek` | `[+-]<val> [<member_id>]` | 控制文件播放、等待音乐或语音输出 | 分词参数 | [L66](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L66) |
| `say` | `<text>` | 控制文件播放、等待音乐或语音输出 | 剩余文本整体传递 | [L67](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L67) |
| `saymember` | `<member_id> <text>` | 控制文件播放、等待音乐或语音输出 | 剩余文本整体传递 | [L68](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L68) |
| `cam` | 空字符串 | 进入摄像画面控制处理器 | 分词参数 | [L69](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L69) |
| `stop` | `<[current\|all\|async\|last]> [<member_id>]` | 控制文件播放、等待音乐或语音输出 | 分词参数 | [L70](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L70) |
| `dtmf` | `<[member_id\|all\|last\|non_moderator]> <digits>` | 控制外呼、成员呼叫或按键 | 成员目标 | [L71](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L71) |
| `kick` | `<[member_id\|all\|last\|non_moderator]> [<optional sound file>]` | 控制外呼、成员呼叫或按键 | 成员目标 | [L72](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L72) |
| `vid-flip` | `<[member_id\|all\|last\|non_moderator]>` | 控制成员视频或会议视频布局 | 成员目标 | [L73](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L73) |
| `vid-border` | `<[member_id\|all\|last\|non_moderator]>` | 控制成员视频或会议视频布局 | 成员目标 | [L74](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L74) |
| `hup` | `<[member_id\|all\|last\|non_moderator]>` | 控制外呼、成员呼叫或按键 | 成员目标 | [L75](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L75) |
| `hold` | `<[member_id\|all]\|last\|non_moderator> [file]` | 控制外呼、成员呼叫或按键 | 成员目标 | [L76](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L76) |
| `unhold` | `<[member_id\|all]\|last\|non_moderator>` | 控制外呼、成员呼叫或按键 | 成员目标 | [L77](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L77) |
| `mute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员音频发送或接收 | 成员目标 | [L78](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L78) |
| `tmute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员音频发送或接收 | 成员目标 | [L79](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L79) |
| `unmute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员音频发送或接收 | 成员目标 | [L80](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L80) |
| `vmute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L81](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L81) |
| `tvmute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L82](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L82) |
| `vmute-snap` | `<[member_id\|all]\|last\|non_moderator>` | 控制成员视频或会议视频布局 | 成员目标 | [L83](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L83) |
| `unvmute` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L84](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L84) |
| `vblind` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L85](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L85) |
| `tvblind` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L86](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L86) |
| `unvblind` | `<[member_id\|all]\|last\|non_moderator> [<quiet>]` | 控制成员视频或会议视频布局 | 成员目标 | [L87](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L87) |
| `deaf` | `<[member_id\|all]\|last\|non_moderator>` | 控制成员音频发送或接收 | 成员目标 | [L88](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L88) |
| `undeaf` | `<[member_id\|all]\|last\|non_moderator>` | 控制成员音频发送或接收 | 成员目标 | [L89](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L89) |
| `vid-filter` | `<[member_id\|all]\|last\|non_moderator> <string>` | 控制成员视频或会议视频布局 | 成员目标 | [L90](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L90) |
| `relate` | `<member_id>[,<member_id>] <other_member_id>[,<other_member_id>] [nospeak\|nohear\|clear]` | 控制成员空间、听说关系或发言选择 | 分词参数 | [L91](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L91) |
| `getvar` | `<varname>` | 读取会议、成员或媒体状态 | 分词参数 | [L92](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L92) |
| `setvar` | `<varname> <value>` | 修改会议变量或参数 | 分词参数 | [L93](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L93) |
| `lock` | 空字符串 | 控制会议入口和PIN | 分词参数 | [L94](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L94) |
| `unlock` | 空字符串 | 控制会议入口和PIN | 分词参数 | [L95](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L95) |
| `dial` | `<endpoint_module_name>/<destination> <callerid number> <callerid name>` | 同步外呼 | 分词参数 | [L96](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L96) |
| `bgdial` | `<endpoint_module_name>/<destination> <callerid number> <callerid name>` | 后台外呼 | 分词参数 | [L97](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L97) |
| `transfer` | `<conference_name> <member id> [...<member id>]` | 控制外呼、成员呼叫或按键 | 分词参数 | [L98](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L98) |
| `record` | `<filename>` | 控制会议录音 | 分词参数 | [L99](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L99) |
| `chkrecord` | `<confname>` | 读取会议、成员或媒体状态 | 分词参数 | [L100](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L100) |
| `norecord` | `<[filename\|all]>` | 控制会议录音 | 分词参数 | [L101](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L101) |
| `pause` | `<filename>` | 暂停录音 | 分词参数 | [L102](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L102) |
| `resume` | `<filename>` | 恢复录音 | 分词参数 | [L103](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L103) |
| `recording` | `[start\|stop\|check\|pause\|resume] [<filename>\|all]` | 控制会议录音 | 分词参数 | [L104](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L104) |
| `exit_sound` | `on\|off\|none\|file <filename>` | 控制成员进出会议提示音 | 分词参数 | [L105](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L105) |
| `enter_sound` | `on\|off\|none\|file <filename>` | 控制成员进出会议提示音 | 分词参数 | [L106](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L106) |
| `pin` | `<pin#>` | 控制会议入口和PIN | 分词参数 | [L107](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L107) |
| `nopin` | 空字符串 | 控制会议入口和PIN | 分词参数 | [L108](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L108) |
| `get` | `<parameter-name>` | 读取会议、成员或媒体状态 | 分词参数 | [L109](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L109) |
| `set` | `<max_members\|sound_prefix\|caller_id_name\|caller_id_number\|endconference_grace_time> <value>` | 修改会议变量或参数 | 分词参数 | [L110](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L110) |
| `file-vol` | `<vol#>` | 控制文件播放、等待音乐或语音输出 | 分词参数 | [L111](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L111) |
| `floor` | `<member_id\|last>` | 控制成员空间、听说关系或发言选择 | 成员目标 | [L112](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L112) |
| `vid-floor` | `<member_id\|last> [force]` | 控制成员视频或会议视频布局 | 成员目标 | [L113](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L113) |
| `vid-banner` | `<member_id\|last> <text>` | 控制成员视频或会议视频布局 | 成员目标 | [L114](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L114) |
| `vid-mute-img` | `<member_id\|last> [<path>\|clear]` | 控制成员视频或会议视频布局 | 成员目标 | [L115](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L115) |
| `vid-logo-img` | `<member_id\|last> [<path>\|clear]` | 控制成员视频或会议视频布局 | 成员目标 | [L116](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L116) |
| `vid-codec-group` | `<member_id\|last> [<group>\|clear]` | 控制成员视频或会议视频布局 | 成员目标 | [L117](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L117) |
| `vid-res-id` | `<member_id>\|all <val>\|clear [force]` | 控制成员视频或会议视频布局 | 分词参数 | [L118](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L118) |
| `vid-role-id` | `<member_id\|last> <val>\|clear` | 控制成员视频或会议视频布局 | 成员目标 | [L119](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L119) |
| `get-uuid` | `<member_id\|last>` | 读取成员通道UUID | 成员目标 | [L120](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L120) |
| `clear-vid-floor` | 空字符串 | 清除视频发言选择 | 剩余文本整体传递 | [L121](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L121) |
| `vid-layout` | `<layout name>\|group <group name> [<canvas id>]` | 控制成员视频或会议视频布局 | 分词参数 | [L122](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L122) |
| `vid-write-png` | `<path>` | 输出视频图像文件 | 分词参数 | [L123](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L123) |
| `vid-fps` | `<fps>` | 控制成员视频或会议视频布局 | 分词参数 | [L124](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L124) |
| `vid-res` | `<WxH>` | 控制成员视频或会议视频布局 | 分词参数 | [L125](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L125) |
| `vid-fgimg` | `<file> \| clear [<canvas-id>]` | 控制成员视频或会议视频布局 | 分词参数 | [L126](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L126) |
| `vid-bgimg` | `<file> \| clear [<canvas-id>]` | 控制成员视频或会议视频布局 | 分词参数 | [L127](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L127) |
| `vid-bandwidth` | `<BW>` | 控制成员视频或会议视频布局 | 分词参数 | [L128](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L128) |
| `vid-personal` | `[on\|off]` | 控制成员视频或会议视频布局 | 分词参数 | [L129](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L129) |

成员分发另检测含“=”的变量选择器。last按成员ID选择，不能只根据附近注释推断新旧顺序。见[成员分发](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L4196)及[变量选择器](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L4239)。

| 全局入口 | 作用域提示 | 来源 |
|---|---|---|
| `conference list` | 未先找到同名会议时分发；同名对象优先级须实测 | [L233](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L233) |
| `conference count` | 未先找到同名会议时分发；同名对象优先级须实测 | [L235](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L235) |
| `conference xml_list` | 未先找到同名会议时分发；同名对象优先级须实测 | [L237](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L237) |
| `conference json_list` | 未先找到同名会议时分发；同名对象优先级须实测 | [L239](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L239) |
| `conference help` | 未先找到同名会议时分发；同名对象优先级须实测 | [L241](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L241) |
| `conference commands` | 未先找到同名会议时分发；同名对象优先级须实测 | [L241](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L241) |

命名会议不存在时dial/bgdial仍有特殊处理；同名会议与list/count/help冲突的优先级须测试。[顶层分发](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/applications/mod_conference/conference_api.c#L218)

## Sofia：已确认分支候选

参数表达式不是完整语法。每项均需测试缺参、额外参数、未知值、对象不存在、并发修改及权限边界。

| 命令路径 | 参数表达式 | 用途和作用域 | 来源 |
|---|---|---|---|
| `sofia help` | 空字符串 | 读取帮助；只读 | [L4541](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4541) |
| `sofia tracelevel` | `[<level>]` | 读取或设置trace日志等级；模块全局 | [L4516](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4516) |
| `sofia loglevel` | `<component> [<numeric_level>]` | 读取或设置库组件日志等级；模块全局；组件集合需补齐 | [L4522](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4522) |
| `sofia global debug` | `[<substring_expression>]` | 调节presence/SLA调试；模块全局；修改状态 | [L4551](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4551) |
| `sofia global siptrace` | `<boolean_expression>` | 切换SIP跟踪；模块全局；修改状态 | [L4578](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4578) |
| `sofia global standby` | `<boolean_expression>` | 切换待机状态；模块全局；修改状态 | [L4584](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4584) |
| `sofia global capture` | `<boolean_expression>` | 切换捕获；模块全局；修改状态 | [L4590](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4590) |
| `sofia global watchdog` | `<boolean_expression>` | 切换看门狗；模块全局；修改状态 | [L4596](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4596) |
| `sofia recover` | `[flush]` | 恢复或清理恢复数据；模块全局；flush清理数据 | [L4621](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4621) |
| `sofia profile restart all` | 空字符串 | 重启所有profile；模块全局；特殊参数顺序 | [L3574](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3574) |
| `sofia profile <name> start` | 空字符串 | 读XML并启动profile；指定profile；gwlist只读，其余通常修改运行状态 | [L3561](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3561) |
| `sofia profile <name> killgw` | `<gateway_name\|_all_>` | 标记网关删除；指定profile；gwlist只读，其余通常修改运行状态 | [L3584](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3584) |
| `sofia profile <name> startgw` | `<gateway_name>` | 载入网关；指定profile；gwlist只读，其余通常修改运行状态 | [L3605](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3605) |
| `sofia profile <name> rescan` | 空字符串 | 重读XML并扫描profile；指定profile；gwlist只读，其余通常修改运行状态 | [L3623](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3623) |
| `sofia profile <name> check_sync` | `[<registration_selector>]` | 同步注册通知；指定profile；gwlist只读，其余通常修改运行状态 | [L3636](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3636) |
| `sofia profile <name> flush_inbound_reg` | `[<registration_selector>] [reboot]` | 清理注册或请求终端重启；指定profile；gwlist只读，其余通常修改运行状态 | [L3649](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3649) |
| `sofia profile <name> recover` | `[flush]` | 恢复或清理profile恢复数据；指定profile；gwlist只读，其余通常修改运行状态 | [L3674](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3674) |
| `sofia profile <name> register` | `<gateway_name\|all>` | 调度网关注册；指定profile；gwlist只读，其余通常修改运行状态 | [L3691](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3691) |
| `sofia profile <name> unregister` | `<gateway_name\|all>` | 调度网关注销；指定profile；gwlist只读，其余通常修改运行状态 | [L3724](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3724) |
| `sofia profile <name> stop` | `[wait]` | 停止profile；指定profile；gwlist只读，其余通常修改运行状态 | [L3756](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3756) |
| `sofia profile <name> restart` | 空字符串 | 重启profile；指定profile；gwlist只读，其余通常修改运行状态 | [L3756](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3756) |
| `sofia profile <name> siptrace` | `<boolean_expression>` | 切换profile跟踪；指定profile；gwlist只读，其余通常修改运行状态 | [L3791](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3791) |
| `sofia profile <name> capture` | `<boolean_expression>` | 切换profile捕获；指定profile；gwlist只读，其余通常修改运行状态 | [L3802](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3802) |
| `sofia profile <name> watchdog` | `<boolean_expression>` | 切换profile看门狗；指定profile；gwlist只读，其余通常修改运行状态 | [L3813](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3813) |
| `sofia profile <name> gwlist` | `[down\|<other_token>]` | 查询网关可用性；指定profile；gwlist只读，其余通常修改运行状态 | [L3825](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3825) |
| `sofia status` | 空字符串 | 读取总体状态；只读 | [L2927](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L2927) |
| `sofia status gateway` | `[<name>]` | 读取网关状态；只读 | [L2993](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L2993) |
| `sofia status profile <name>` | 空字符串 | 读取profile详情；只读 | [L3028](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3028) |
| `sofia status profile <name> pres` | `<presence_selector>` | 查询presence信息；只读 | [L3119](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3119) |
| `sofia status profile <name> reg` | `[<contact_selector>]` | 查询注册记录；只读 | [L3125](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3125) |
| `sofia status profile <name> user` | `<user@domain>` | 查询用户记录；只读 | [L3137](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3137) |
| `sofia xmlstatus` | 空字符串 | 读取总体状态；只读 | [L3287](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3287) |
| `sofia xmlstatus gateway` | `[<name>]` | 读取网关状态；只读 | [L3320](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3320) |
| `sofia xmlstatus profile <name>` | 空字符串 | 读取profile详情；只读 | [L3329](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3329) |
| `sofia xmlstatus profile <name> pres` | `<presence_selector>` | 查询presence信息；只读 | [L3421](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3421) |
| `sofia xmlstatus profile <name> reg` | `[<contact_selector>]` | 查询注册记录；只读 | [L3428](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3428) |
| `sofia xmlstatus profile <name> user` | `<user@domain>` | 查询用户记录；只读 | [L3442](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3442) |
| `sofia jsonstatus` | 空字符串 | 读取全局JSON状态；只读 | [L192](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/sofia_json_api.c#L192) |

### 必须保留的边界

1. `sofia profile restart all`的特殊顺序已由解析分支确认，不能改写成profile all restart。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3574)
2. killgw批量关键字为_all_，register/unregister为all。显式名称查找与批量遍历的作用域还需实测。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3584)
3. stop wait是有上限的等待循环，并非本标准中等待所有活动呼叫自然结束的无损排空。stop/restart还会拒绝启动不足10秒的profile。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L3756)
4. global debug按presence/sla/none子串检查；布尔值通过switch_true解释。说明里的常用词不是解析接受范围的穷举。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4551)
5. status/xmlstatus只有一个参数时进入网关概要分支；未知单token也应差分测试。pres/reg/user另有不同查询路径。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L2939)
6. jsonstatus函数体不按argv/argc过滤。JSON API sofia.status与sofia.status.info是独立注册面，不要直接套用status命令树。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/sofia_json_api.c#L192)
7. recover处理恢复数据，返回成功不能推导出媒体、应用或ESL连接无中断迁移。[来源](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4621)

### 仅在帮助中发现的候选

| 帮助路径 | 参数 | 状态 | 来源 |
|---|---|---|---|
| `sofia profile <name> stun-auto-disable` | `[true\|false]` | 仅帮助；未确认解析分支 | [L4482](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4482) |
| `sofia profile <name> stun-enabled` | `[true\|false]` | 仅帮助；未确认解析分支 | [L4482](https://github.com/signalwire/freeswitch/blob/ef32e205295e29f034f1453ad245ba5efb07b94a/src/mod/endpoints/mod_sofia/mod_sofia.c#L4482) |

本次cmd_profile核对未找到上述两项对应解析分支；必须在v1.11.3基线执行并保存实际响应，不能依据帮助字符串创建假成功实现。

## 待补验收记录

每个条目补齐：调用入口和身份、完整输入、前置对象状态、原始响应、事件与副作用、完成时点、错误/重复/并发结果、基线差异。通过运行证据后才能更新not_verified状态。

[机器可读目录](conference-and-sofia-subcommands.json)
