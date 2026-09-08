#!/usr/bin/env python3
# coding: utf-8
"""生成固定FreeSWITCH基线的逐模块功能审计；只验证静态覆盖，不执行兼容或容量认证。"""
import argparse
import ast
from collections import Counter, defaultdict
import csv
import hashlib
import json
from pathlib import Path
import re
import sys
import urllib.request
import xml.etree.ElementTree as ET
from build_runtime_verification import GO_NATIVE_SUFFIXES

ROOT = Path(__file__).resolve().parents[1]
CATALOG = ROOT / 'docs/freeswitch-compatibility/catalog'
COMMIT = 'ef32e205295e29f034f1453ad245ba5efb07b94a'
VERSION = '1.11.3'
AUDIT_DATE = '2026-09-06'
SOURCE_URL = 'https://github.com/signalwire/freeswitch/blob/' + COMMIT + '/'
TREE_URL = 'https://github.com/signalwire/freeswitch/tree/' + COMMIT + '/'
# 最新版查询是有日期的官方网页观察；离线复验不会偷偷把它改写为当前日期。
LATEST = {'checked_on': '2026-09-05', 'observed_latest_release': 'v1.11.3', 'reference_matches_observed_latest': True,
          'source_url': 'https://github.com/signalwire/freeswitch/releases/latest',
          'resolved_url': 'https://github.com/signalwire/freeswitch/releases/tag/v1.11.3',
          'method': '官方GitHub releases/latest网页实际跳转及Latest标记；固定提交另与已保存完整树和逐文件哈希核对',
          'api_draft_and_prerelease_flags': None,
          'limitation': '这是2026-09-05的最新正式发行观察；离线--check不重新联网，也不把master后续提交加入基线。现场发行包、编译选项和第三方模块仍未采集。'}
CATEGORIES = {'applications':'业务应用','asr_tts':'语音识别与合成','codecs':'编解码接口','databases':'数据库驱动',
              'dialplans':'拨号计划','directories':'目录后端','endpoints':'协议与设备端点','event_handlers':'事件与话单',
              'formats':'媒体文件与流','languages':'脚本语言宿主','loggers':'日志后端','say':'语言播报','timers':'定时器','sdk':'SDK示例','xml_int':'XML集成'}
STATUS_LABELS = {'host_not_implemented':'原模块宿主未实现','partial_protocol':'仅部分协议用途',
                 'analogous_not_wire_compatible':'功能相近，接口不兼容','export_only':'仅编辑导出配置',
                 'sdk_example_only':'SDK示例，非业务模块','build_stub_only':'仅构建目录，运行产物待采集'}
# 每个目录必须有明确业务说明；新增目录没有人工归类时直接失败，不能自动计为兼容。
MODULE_ROWS = '''
autotools|外部模块autotools开发示例，源码定义mod_example|native
mod_alsa|ALSA本机音频终端|endpoints
mod_amqp|AMQP事件发布及命令集成|events
mod_amr|AMR媒体编解码接口|codecs
mod_amrwb|AMR-WB媒体编解码接口|codecs
mod_av|FFmpeg音视频格式与编解码集成|video
mod_avmd|语音信号中的提示音检测|ivr
mod_b64|Base64示例媒体编码接口|codecs
mod_basic|BASIC解释器及会话API宿主|scripts
mod_bert|媒体误码测试应用|diagnostics
mod_blacklist|黑名单查询与管理应用|routing
mod_bv|BroadVoice媒体编解码接口|codecs
mod_callcenter|呼叫中心队列、坐席及分配策略|queues
mod_cdr_csv|CSV话单输出及模板字段|cdr
mod_cdr_pg_csv|PostgreSQL与CSV话单输出|cdr
mod_cdr_sqlite|SQLite话单输出|cdr
mod_cidlookup|主叫身份信息查询|routing
mod_cluechoo|cluechoo示例应用与API|diagnostics
mod_codec2|Codec2媒体编解码接口|codecs
mod_com_g729|完整树中的G.729构建目录，仅有Makefile.am|codecs
mod_commands|管理API、UUID命令与状态查询|commands
mod_conference|会议成员、混音、控制与录制|conference
mod_console|FreeSWITCH控制台日志输出|logging
mod_curl|拨号应用与API中的HTTP请求|integrations
mod_cv|视频运动检测与计算机视觉处理|video
mod_db|核心键值存储相关API与应用|databases
mod_dialplan_asterisk|Asterisk风格拨号计划解析|xml
mod_dialplan_directory|目录驱动的拨号计划|xml
mod_dialplan_xml|XML拨号计划条件与应用执行|xml
mod_directory|按姓名查询分机的目录业务|ivr
mod_distributor|加权分配与节点选择|routing
mod_dptools|answer、bridge、playback等拨号应用集合|ivr
mod_easyroute|数据库驱动的简易路由|routing
mod_enum|ENUM号码解析与路由|routing
mod_erlang_event|Erlang事件与控制接口|events
mod_esf|附加SIP相关应用|sip
mod_esl|从FreeSWITCH业务内使用ESL客户端|esl
mod_event_multicast|组播事件传输|events
mod_event_socket|入站与出站Event Socket控制协议|esl
mod_event_test|事件系统测试模块|diagnostics
mod_expr|表达式计算API|routing
mod_fail2ban|Fail2ban相关安全事件对接|events
mod_fifo|FIFO呼叫队列与接听分配|queues
mod_flite|Flite本地语音合成|speech
mod_format_cdr|可配置格式的话单生成|cdr
mod_fsk|FSK信号与数据处理|fax
mod_fsv|FreeSWITCH视频文件录制与播放|recording
mod_g723_1|G.723.1媒体负载接口|codecs
mod_g729|G.729媒体负载接口，不能仅凭模块名推断可转码|codecs
mod_graylog2|Graylog日志对接|logging
mod_h323|H.323协议端点|endpoints
mod_hash|哈希存储与资源限制后端|databases
mod_hiredis|Hiredis支持的数据与资源限制后端|databases
mod_httapi|HTTP驱动的交互式电话业务协议|integrations
mod_http_cache|HTTP媒体对象缓存|formats
mod_ilbc|iLBC媒体编解码接口|codecs
mod_imagick|ImageMagick图像格式集成|video
mod_java|Java虚拟机与会话API宿主|scripts
mod_json_cdr|JSON话单生成与投递|cdr
mod_lcr|最小成本路由与运营商选择|routing
mod_ldap|LDAP目录后端|integrations
mod_limit|会话资源限制API、应用及后端分派|admission
mod_local_stream|本地媒体流与背景音源|formats
mod_logfile|文件日志、格式与轮转|logging
mod_loopback|内部loopback会话端点|endpoints
mod_lua|Lua会话、API与XML处理宿主|scripts
mod_managed|托管语言与会话API宿主|scripts
mod_mariadb|MariaDB数据库驱动|databases
mod_memcache|Memcached缓存接口|databases
mod_mongo|MongoDB数据接口|databases
mod_native_file|原生编码媒体文件格式|formats
mod_nibblebill|通话计费与余额控制|billing
mod_odbc_cdr|ODBC话单输出|cdr
mod_opal|OPAL协议栈端点|endpoints
mod_openh264|OpenH264视频编码接口|video
mod_opus|Opus媒体编解码接口|codecs
mod_opusfile|Opus文件与流格式|formats
mod_osp|OSP鉴权、路由与结算集成|billing
mod_perl|Perl解释器及会话API宿主|scripts
mod_pgsql|PostgreSQL数据库驱动|databases
mod_png|PNG图像文件接口|video
mod_pocketsphinx|PocketSphinx语音识别|speech
mod_posix_timer|POSIX媒体定时器实现|timers
mod_prefix|前缀树查询与管理|routing
mod_python3|Python3解释器及会话API宿主|scripts
mod_random|随机值相关API|routing
mod_redis|Redis数据与资源限制接口|databases
mod_reference|端点开发参考实现|native
mod_rtc|rtc端点接口与媒体会话|endpoints
mod_rtmp|RTMP协议端点与音视频会话|video
mod_say_de|德语数字、时间等语言播报规则|speech
mod_say_en|英语数字、时间等语言播报规则|speech
mod_say_es|西班牙语语言播报规则|speech
mod_say_es_ar|阿根廷西班牙语语言播报规则|speech
mod_say_fa|波斯语语言播报规则|speech
mod_say_fr|法语语言播报规则|speech
mod_say_he|希伯来语语言播报规则|speech
mod_say_hr|克罗地亚语语言播报规则|speech
mod_say_hu|匈牙利语语言播报规则|speech
mod_say_it|意大利语语言播报规则|speech
mod_say_ja|日语语言播报规则|speech
mod_say_nl|荷兰语语言播报规则|speech
mod_say_pl|波兰语语言播报规则|speech
mod_say_pt|葡萄牙语语言播报规则|speech
mod_say_ru|俄语语言播报规则|speech
mod_say_sv|瑞典语语言播报规则|speech
mod_say_th|泰语语言播报规则|speech
mod_say_zh|中文数字、时间等语言播报规则|speech
mod_shell_stream|外部进程媒体流输入|formats
mod_shout|SHOUT及流式音频格式集成|formats
mod_signalwire|SignalWire服务集成|integrations
mod_silk|SILK媒体编解码接口|codecs
mod_siren|Siren语音编码接口|codecs
mod_skel|应用模块开发骨架|native
mod_skel_codec|编解码模块开发骨架|native
mod_skinny|Skinny/SCCP协议端点|endpoints
mod_smpp|SMPP消息协议集成|messaging
mod_sms|文本消息与聊天计划应用|messaging
mod_snapshot|通话媒体片段捕获|recording
mod_sndfile|libsndfile音频文件读写|formats
mod_snmp|SNMP运行指标接口|events
mod_sofia|Sofia SIP端点、profile与gateway|sip
mod_spandsp|SpanDSP传真及信号处理|fax
mod_spy|通话监听与监控业务|ivr
mod_syslog|系统日志输出|logging
mod_test|业务与媒体测试模块|diagnostics
mod_timerfd|Linux timerfd媒体时钟|timers
mod_tone_stream|音调媒体流生成|formats
mod_translate|号码及字符串转换规则|routing
mod_tts_commandline|外部命令语音合成|speech
mod_v8|V8 JavaScript与会话API宿主|scripts
mod_valet_parking|驻留、取回与停车位控制|ivr
mod_verto|Verto/WebSocket电话控制及浏览器会话|webrtc
mod_video_filter|视频过滤与处理|video
mod_vlc|VLC媒体文件与流集成|formats
mod_vmd|语音信箱提示音检测|ivr
mod_voicemail|语音信箱存储、通知与访问|voicemail
mod_voicemail_ivr|语音信箱交互菜单|voicemail
mod_webm|WebM音视频容器格式|video
mod_xml_cdr|XML话单生成与投递|cdr
mod_xml_curl|HTTP获取动态XML配置、目录及拨号计划|xml_curl
mod_xml_ldap|LDAP驱动的XML查询|integrations
mod_xml_rpc|XML-RPC及HTTP控制入口|commands
mod_xml_scgi|SCGI动态XML查询|integrations
mod_yuv|YUV视频媒体接口|video
'''
MODULE_META = {name: {'purpose': purpose, 'domain': domain} for name, purpose, domain in
               (line.split('|') for line in MODULE_ROWS.strip().splitlines())}
# 相近功能只说明有限用途重叠，全部条目仍明确没有原FreeSWITCH模块宿主和线协议认证。
RELATIONS = {
 'mod_sofia': ('partial_protocol','自研Go IPv4 SIP UDP/TCP/TLS可信中继支持六种入站方法、本地精确号码应答、固定上游REGISTER/Digest与认证呼叫；无终端注册服务器、多网关宿主、WS/WSS或完整Sofia扩展。',['sip_dispatch','sip_transport','sdp_limits','protocol_abi']),
 'mod_commands': ('partial_protocol','ESL api/bgapi有限分派：echo/create_uuid/version/status/show api as json及九个uuid命令（含受限uuid_send_dtmf及自有uuid_send_dtmf_status；不将扩展计作原版同名API）；并非原版全部API，status保留自有格式。',['esl_api','guard','native_abi']),
 'mod_event_socket': ('partial_protocol','入站ESL认证、分帧、api/bgapi、受限sendmsg execute及通道/应用/播放/停泊实际事件子集；无outbound、userauth与完整事件语义。',['esl_transport','esl_api']),
 'mod_dptools': ('partial_protocol','已有answer/set/unset/read/playback/sleep/park/hangup受限队列、实际WAV/提示音和收号；无完整原版应用、IVR菜单、媒体图或park抢占。',['sip_dispatch','media_commands','xml_export']),
 'mod_limit': ('analogous_not_wire_compatible','自有并发/建立中/CPS准入策略及503反馈；不是limit应用、原后端接口或realm/resource计数语义。',['guard','sip_dispatch']),
 'mod_console': ('analogous_not_wire_compatible','Go进程日志可供本机运维观察；不是原控制台、日志字段或fs_cli交互协议。',['go_main','journal','native_abi']),
 'mod_logfile': ('analogous_not_wire_compatible','自有JSONL事件持久化及进程日志有部分记录用途；不是mod_logfile配置、级别、轮转或CDR格式。',['journal','native_abi']),
 'mod_timerfd': ('analogous_not_wire_compatible','Rust事件循环与Go可取消定时器用于当前转发原型；没有FreeSWITCH timer接口和媒体时钟合同。',['media_io','timers','native_abi']),
 'mod_posix_timer': ('analogous_not_wire_compatible','自有调度与定时器只服务内部状态机；不能加载原POSIX timer模块或承诺相同媒体时钟。',['timers','media_commands','native_abi']),
 'mod_dialplan_xml': ('partial_protocol','可启动冻结受限XML：指定context、首个destination_number RE2单条件、8个限定应用顺序执行，未命中404；无预处理、多条件、变量展开、动态绑定或reloadxml。',['xml_export','config','sip_dispatch'])
}
# 文件及锚点是本次人工审查依据，生成时定位到当前行号并保存哈希，源码变化不会静默沿用旧证据。
EVIDENCE = {
 'sip_dispatch': ('control/internal/server/calls.go','switch m.Method {','仅分派列出的SIP请求；默认405，re-INVITE明确501。'),
 'sip_transport': ('control/internal/server/transport.go','type sipFlow','IPv4 SIP UDP/TCP/TLS双腿，连接身份和有界队列；WS/WSS、IPv6及完整SIPS路由未实现。'),
 'esl_transport': ('control/internal/esl/server.go','func Listen','可选回环入站ESL、固定执行器与连接队列预算；完整原版Event Socket仍待补齐。'),
 'esl_api': ('control/internal/server/esl_api.go','func (s *Server) CompatibilityAPI','有限API与按UUID索引的主循环操作，未知命令不虚报成功。'),
 'sdp_limits': ('control/internal/sip/sdp.go','SDP feature not supported by UDP prototype','SDP拒绝保持方向、ICE、SRTP与RTCP复用等超出原型范围的协商。'),
 'sdp_codecs': ('control/internal/sip/sdp.go','func parseMapping','控制面区分PCMU/PCMA、G722双时钟、Opus、G729、G726/AAL2各码率及L16；协商声明、worker接收、逐向有效媒体与跨编码转码必须分别验证。'),
 'codec_profiles': ('control/internal/codecprofile/profiles.go','func List()','测试格式列出载荷、采样率、RTP时钟、声道、ptime与fmtp；固定传输向量不是音质或原版编解码模块认证。'),
 'codec_answer': ('control/internal/sip/sdp.go','func NegotiateAnswer','同格式透传应答检查；不接受要求PT改写、重分包或不同编码转换的应答。'),
 'http_admin': ('control/internal/server/server.go','net.Listen("tcp4", c.Admin.Listen)','当前TCP入口为自有HTTP管理服务，不是Event Socket监听。'),
 'config': ('control/internal/config/config.go','type Config struct','运行配置是自有JSON结构，没有FreeSWITCH模块加载及XML执行入口。'),
 'xml_export': ('control/internal/server/fs_config.go','这些模板用于配置编辑/导出','官方XML仅作为编辑/导出产物，配置字段不进入运行功能完成分子。'),
 'native_abi': ('native/include/rustswitch_codec.h','不兼容 FreeSWITCH','C插件使用自定义ABI，不能原样加载FreeSWITCH mod_*.so。'),
 'protocol_abi': ('native/include/rustswitch_protocol.h','尚未实现 Sofia-SIP/PJSIP','原生协议适配器仅预留合同，没有实际Sofia/PJSIP提供器。'),
 'codec_mode': ('media/src/main.rs','conflicts_with = "check_codec"','C codec往返检查与worker运行模式互斥，不在现有通话转发路径调用转码器。'),
 'codec_loader': ('media/src/codec.rs','rs_codec_get_v1','动态库读取自有描述符入口，不提供switch_*宿主ABI。'),
 'media_commands': ('media/src/media/worker.rs','Command::Allocate {','媒体命令包含分配、连接、释放、统计、关闭及按键游标/提示音；未提供混音、录音或完整IVR执行图。'),
 'media_io': ('media/src/media/io.rs','libc::recvmmsg(','Linux批量接收优化已在源码中；单个系统调用优化不能证明全面性能优于FreeSWITCH。'),
 'rtp': ('media/src/media/rtp.rs','pub fn','RTP/RTCP校验与转发实现，不等于终结型媒体会话、完整质量评估或DTLS/SRTP。'),
 'guard': ('control/internal/server/guard.go','func (g *AdmissionGuard) TryAdmit','自有准入策略只作用于新呼叫，语义不等于FreeSWITCH的全部limit后端。'),
 'timers': ('control/internal/server/timers.go','type timer','Go内部呼叫与事务定时器，不是FreeSWITCH媒体timer插件接口。'),
 'journal': ('control/internal/journal/journal.go','type Event struct','自有JSONL事件模型，不是FreeSWITCH事件头或完整CSV/XML/JSON话单。'),
 'go_main': ('control/cmd/rustswitch/main.go','func main()','自有CLI、部署与日志入口，不是freeswitch或fs_cli的全部选项与服务行为。'),
 'benchmark': ('control/internal/server/benchmark_runner.go','func (m *benchmarkManager) runReal','测试创建本机独立实例，实际负载与生产容量认证分离。'),
 'callbench': ('control/cmd/callbench/main.go','report := map[string]any','发生器报告记录真实建呼与双向收包边界；不是原版FreeSWITCH成对差分结果。'),
 'e2e': ('tests/e2e.py','def test_bidirectional_rtp_rtcp_dtmf_and_cleanup','项目自有回归用例存在；本审计只静态枚举，不把定义计作本轮执行通过。'),
 'failure': ('media/src/media/worker.rs','EOF 会关闭命令通道','控制管道断开使媒体退出；进程隔离并未实现控制面崩溃后的存量通话无损接管。')
}
# 核心业务域不局限于src/mod目录，避免把内建G.711、XML与会话核心遗漏在模块表之外。
DOMAIN_ROWS = [
 ('native','原生模块宿主与客户端ABI','missing','自有codec ABI可独立检查，原FreeSWITCH模块、switch_*宿主和libesl对象语义未实现。',['native_abi','codec_loader','protocol_abi'],'原样加载启用模块及第三方产物；比对导出符号、内存所有权、回调顺序、卸载和异常。'),
 ('sip','SIP中继与完整Sofia行为','partial','支持可信固定中继六种入站方法、UDP/TCP/TLS、受限上游注册/Digest和本地自动应答；终端注册、复杂路由和完整Sofia状态机缺失。',['sip_dispatch','sip_transport','sdp_limits'],'比较正常、拒接、重传、CANCEL/200竞争、早期媒体、Route、PRACK、REFER、重协商与分叉报文。'),
 ('registration','用户注册、Digest与权限','partial','固定上游REGISTER客户端与Digest认证呼叫已实现；无终端注册数据库、位置服务或用户目录鉴权。',['sip_dispatch','config'],'注册刷新/注销、nonce过期、重放、认证失败、同号多终端、域隔离与注册故障恢复。'),
 ('secure_transport','TCP/TLS与IPv6信令','partial','已实现IPv4 TCP/TLS双腿、证书校验与连接身份；IPv6、DNS/SRV、客户端证书及完整SIPS边界未实现。',['sip_transport','protocol_abi'],'相同证书及传输策略测试握手、认证、TLS错误、连接关闭、DNS/SRV与IPv6往返。'),
 ('endpoints','H.323、设备与内部端点','missing','没有H.323/OPAL、Skinny、ALSA设备和FreeSWITCH loopback/rtc端点宿主；本机发生器不是这些端点实现。',['sip_transport','protocol_abi','native_abi'],'逐已启用端点使用原客户端和设备测试建立、媒体、转接、断连、驱动失败及内部会话生命周期。'),
 ('webrtc','WebRTC、Verto与安全媒体','missing','未实现WebSocket/Verto、ICE/STUN/TURN、DTLS/SRTP和RTCP复用。',['sip_transport','sdp_limits','protocol_abi'],'原浏览器客户端不改配置连通；采集ICE、DTLS、SRTP、重协商、NAT变化与断网恢复。'),
 ('media','RTP/RTCP与DTMF','partial','单音频流同编码转发、独立RTCP与telephone-event；控制面已扩展明确编码身份、双时钟及CN。每种格式的worker接收与实际双向结果须联验，不能从SDP列表推导全部终端兼容。',['sdp_codecs','codec_answer','rtp','media_commands'],'逐方向校验RTP/RTCP、DTMF事件和序列；覆盖丢包、乱序、重复、抖动、恶意包及端点变更。'),
 ('codecs','多编码、重采样与转码通话','partial','已有多编码SDP与测试格式，不能沿用仅PT0/8的控制协商结论；跨编码通话、重采样、重新分包及原FreeSWITCH codec宿主仍无完整验收。自有C ABI和新增原生音频实现需各自提供构建、自检及真实通话证据。',['sdp_codecs','codec_profiles','codec_answer','codec_mode','codec_loader'],'按现场编码矩阵完成真实通话、重采样、ptime变换、质量与CPU对比，核对授权依赖。'),
 ('ivr','IVR、放音、收号与通话应用','partial','本地号码/XML受限路由、WAV及G711提示音、RTP/INFO read、park与播放打断已实现；无完整IVR菜单、监听、停车检索或完整转接。',['media_commands','xml_export','sip_dispatch'],'原IVR菜单、playback/read、超时、打断、DTMF、失败分支、转接和事件轨迹逐项对跑。'),
 ('conference','会议与混音','missing','没有会议成员状态、音频混合、静音/能量检测、布局或会议录制。',['media_commands','sdp_limits'],'多人入退会、混音、静音、音量、录制、事件及跨编码会议完整对跑，单独压测会议负载。'),
 ('recording','通话录音与媒体捕获','missing','没有录音应用、媒体bug、文件格式、双声道、暂停续录与落盘故障语义。',['media_commands','codec_mode'],'核对录音起止时间、采样格式、双腿/双声道、异常断话、磁盘满、断电与文件可播放性。'),
 ('voicemail','语音信箱','missing','没有语音信箱存储、交互菜单、邮件通知、MWI及用户访问流程。',['media_commands','xml_export'],'原用户登录、留言、播放删除、配额、通知、MWI、错误码和磁盘恢复成对验收。'),
 ('queues','呼叫中心、FIFO与坐席','missing','没有坐席状态、队列策略、等待媒体、分配、超时或队列持久化。',['config','sip_dispatch'],'多队列多坐席、重入、抢接、溢出、取消、接通竞争与重启恢复，核对统计和CDR。'),
 ('routing','路由、号码转换与业务查询','partial','运行支持固定上游、本地精确号码和冻结XML目的号码条件；没有LCR、ENUM、动态分配与完整dialplan变量语义。',['config','sip_dispatch','xml_export'],'原号码规则及外部查询在相同失败条件下比对选择、重试、计费前缀和变量作用域。'),
 ('xml','XML配置、directory与dialplan','partial','官方配置可编辑导出；可选冻结XML执行受限号码条件和顺序应用，不含完整预处理、变量展开、嵌套条件、anti-action或inline。',['xml_export','config'],'原XML不修改加载；比对include、$${}/${}、目录查询、嵌套条件、break/continue和热重载。'),
 ('xml_curl','动态XML HTTP回调','missing','没有mod_xml_curl的请求字段、编码、缓存、绑定顺序、错误和fallback执行。',['xml_export','config'],'用同一个可控HTTP后端核对请求字节、字段、超时、HTTP状态、not found、缓存与本地fallback。'),
 ('scripts','Lua、JavaScript、Python等脚本','missing','没有原语言运行时和会话API宿主；可编辑脚本配置不意味着原脚本会执行。',['native_abi','media_commands','xml_export'],'原脚本及库不改运行；核对Session/API/Event、XML handler、回调、GC、异常和并发隔离。'),
 ('esl','Event Socket、fs_cli与libesl','partial','已实现入站ESL认证、有限api/bgapi及真实事件子集；outbound、完整execute参数/事件与libesl对象ABI未完成。',['esl_transport','esl_api','native_abi'],'原fs_cli/libesl无修改连接，逐字节比较认证、命令、异步结果、事件过滤/排序和断连恢复。'),
 ('events','事件接口与外部消息系统','partial','入站ESL有真实通道/应用/播放/停泊事件子集；全部事件字段、CUSTOM子类和AMQP等消息合同仍未完成。',['journal','http_admin'],'核对事件全字段、偏序、订阅过滤、重复、背压、断线与外部AMQP/组播/SNMP接口。'),
 ('cdr','话单、计数与投递','missing','JSONL事件日志不是FreeSWITCH CSV/XML/JSON/数据库CDR，字段与交付语义尚未实现。',['journal','xml_export'],'核对接听与挂断时点、原因、字段、文件/HTTP/数据库格式、重试、重复和重启对账。'),
 ('databases','数据库、缓存和目录后端','missing','没有PostgreSQL/MariaDB、Redis、Mongo、Memcache及模块数据API兼容。',['config','native_abi'],'同一数据集验证查询、事务、连接故障、重试、字符集、作用域和连接池压力。'),
 ('integrations','HTTAPI与外部业务接口','missing','没有HTTAPI、SCGI、LDAP、SignalWire等原业务协议；HTTP管理页不是这些业务接口。',['http_admin','xml_export'],'原外部业务系统零改动对接，核对HTTP/XML字节、认证、状态、超时、重试与副作用。'),
 ('billing','实时计费与运营商结算','missing','没有nibblebill或OSP的余额控制、实时扣费、路由授权和结算语义。',['config','journal'],'余额边界、并发扣费、断话退款、结算幂等、后端失败与CDR账目守恒。'),
 ('fax','传真、FSK与信号处理','missing','没有T.38传真或SpanDSP/FSK业务执行；透明G.711转发不能替代传真端到端验收。',['sdp_limits','media_commands'],'同一传真终端/测试页验证音频传真及T.38切换、ECM、超时、结果码和文件输出。'),
 ('video','视频、图像与音视频容器','missing','SDP只接受单音频流，未实现视频编解码、视频会议、过滤、录制或图像格式。',['sdp_limits','media_commands'],'多视频/音频流、H.264/VPx、容器读写、分辨率、布局、关键帧和同步逐场景对跑。'),
 ('messaging','文本消息、聊天计划与MSRP','missing','SIP MESSAGE等未列入方法分派；没有SMS/SMPP、MSRP文件传输和chatplan业务。',['sip_dispatch','xml_export'],'原MESSAGE/SMPP/MSRP客户端测试编码、路由、附件、离线、重试与安全失败。'),
 ('formats','文件、音源与流式媒体','missing','没有FreeSWITCH文件接口、音调/背景音源、HTTP缓存或VLC/SHOUT等执行层。',['media_commands','codec_mode'],'逐格式验证寻址、暂停、seek、循环、采样转换、流中断、缓存和文件错误。'),
 ('speech','ASR、TTS与语言播报','missing','没有语音识别/合成引擎或say语言规则执行；目录包含语言参数不计功能实现。',['media_commands','xml_export'],'原识别语法与语种、播报词法、数字日期、打断、引擎故障和音频输出对比。'),
 ('commands','CLI、API与服务管理','analogous','自有HTTP与CLI覆盖部分管理用途，不兼容原命令名、参数、文本输出及启动行为。',['http_admin','go_main','native_abi'],'逐注册API/应用及实际动态接口核对参数、输出、状态、错误、异步事件和服务启动/退出。'),
 ('logging','日志格式与运维集成','analogous','自有进程日志和JSONL有记录用途，尚未适配原日志模块格式、轮转及消费者。',['journal','go_main'],'原日志采集器不改解析，验证格式、级别、轮转、阻塞、磁盘故障及丢失统计。'),
 ('admission','准入、过载与业务保护','analogous','自有热更新并发/建立中/CPS保护可用；没有证明相较FreeSWITCH在相同负载更优。',['guard','benchmark'],'相同容量与业务策略成对比较准入、503/Retry-After、已建立通话、公平性和恢复。'),
 ('timers','媒体时钟与调度','analogous','Go定时器堆和Rust就绪循环为内部实现；未兼容FreeSWITCH timer模块接口。',['timers','media_io'],'核对长短定时器、取消竞态、漂移、时钟调整、音频节奏和满载尾延迟。'),
 ('diagnostics','诊断与测试模块','analogous','自有callbench和管理测试可做本机模拟；不是FreeSWITCH诊断模块或动态兼容差分器。',['benchmark','callbench','e2e'],'运行原诊断API并对比输出，另验证差分器能够抓到故意注入的漏包、漏事件和错腿。'),
 ('reliability','故障、排空、在线迁移与恢复','partial','可排空及分片重启，但故障分片通话会丢失；控制进程故障后的无损保持和跨机接管未实现。',['failure','journal','benchmark'],'分别验收drain、新呼叫切换、live在途接管、回滚、控制/媒体/整机故障及话单RPO/RTO。'),
 ('performance','万路容量及是否全面超越','not_verified','未提供同硬件同业务的FreeSWITCH成对性能结果；历史万路建成不等于万路媒体满负载通过。',['media_io','callbench','benchmark'],'同硬件内核网卡及独立发生器，以相同编解码、转码/会议/录音/ESL负载比较CPS、并发、丢包、延迟、CPU/RSS和故障恢复。')
]


def require(condition, message):
    """审计门禁显式抛错，Python优化模式也不能绕过分母和哈希验证。"""
    if not condition:
        raise ValueError(message)


def digest(data):
    """统一使用SHA-256记录输入，Git blob校验另外保留其原始对象算法。"""
    return hashlib.sha256(data).hexdigest()


def uncomment(text):
    """保留字符串和行号，仅去掉注释，避免注释里的模块宏被误计为运行声明。"""
    token = re.compile(r'"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\'|/\*[\s\S]*?\*/|//[^\n]*')
    return token.sub(lambda m: re.sub(r'[^\n]', ' ', m[0]) if m[0].startswith(('/*','//')) else m[0], text)


def validate_denominator(entries, expected):
    """每个指定目录必须且只能出现一次；未知项与遗漏项同时报告。"""
    names = [entry['module'] for entry in entries]
    require(len(names) == len(set(names)), '重复模块审计条目')
    require(set(names) == set(expected), '目录分母不一致：缺少=%s；多出=%s' % (sorted(set(expected)-set(names)), sorted(set(names)-set(expected))))


def locate(path, needle, description, base=ROOT):
    """按已审查文本定位真实源码；找不到锚点即要求重新审查，不能生成假行号。"""
    data = (base/path).read_bytes()
    text = data.decode('utf-8',errors='replace')
    require(needle in text, '审查锚点已变化：'+path+' / '+needle)
    return {'path':path, 'line':text.count('\n',0,text.index(needle))+1, 'sha256':digest(data), 'description':description}


def verify_sources(source, tree_path):
    """逐一复核712份基线文件、Git blob及完整树元数据，并提取全树mod_*目录。"""
    manifest = json.loads((CATALOG/'reference-source-manifest.json').read_text())
    tree = json.loads(tree_path.read_text())
    require(manifest['commit']==COMMIT and not manifest['errors'], '参考文件清单版本或下载状态异常')
    require(tree['sha']==COMMIT and tree.get('truncated') is False, '必须提供固定提交的完整递归Git树')
    blobs = {item['path']:item for item in tree['tree'] if item['type']=='blob'}
    sources = {}
    for item in manifest['files']:
        data=(source/item['path']).read_bytes()
        require(digest(data)==item['sha256'], '参考SHA-256不一致：'+item['path'])
        blob=hashlib.sha1(b'blob '+str(len(data)).encode()+b'\0'+data).hexdigest()
        require(blob==item['git_blob_sha1']==blobs[item['path']]['sha'], '参考Git blob不一致：'+item['path'])
        sources[item['path']]=data.decode('utf-8',errors='replace')
    modules={}
    for item in tree['tree']:
        parts=item['path'].split('/')
        if len(parts)==4 and parts[:2]==['src','mod'] and parts[3].startswith('mod_') and item['type']=='tree':
            modules[parts[3]]={'category':parts[2],'path':item['path'],'tree_sha1':item['sha']}
    require(len(sources)==712, '固定选定文件分母变化，须人工更新审计基线')
    require(len(modules)==144, '完整树模块目录分母变化，须人工更新')
    return manifest,tree,sources,modules


def definitions(sources):
    """仅扫描实际模块定义宏调用，不执行预处理、链接或加载，因此结果仍是源码声明。"""
    found=defaultdict(list)
    for path,raw in sorted(sources.items()):
        if not path.startswith('src/mod/'):
            continue
        clean=uncomment(raw)
        for match in re.finditer(r'\bSWITCH_MODULE_DEFINITION(?:_EX)?\s*\(\s*(mod_\w+)',clean):
            start=clean.rfind('\n',0,match.start())+1
            if clean[start:match.start()].lstrip().startswith('#'):
                continue
            line=raw.count('\n',0,match.start())+1
            found[path.split('/')[3]].append({'name':match[1],'path':path,'line':line,'source_url':SOURCE_URL+path+'#L'+str(line)})
    return found


def configured_modules(sources):
    """解析未被注释的vanilla模块加载项与相关XML；配置存在不能证明二进制可构建或已加载。"""
    loaded=[]
    config_files=defaultdict(set)
    for path,raw in sources.items():
        if not path.startswith('conf/vanilla/') or not path.endswith('.xml'):
            continue
        # 原版include片段可包含多个顶层节点；虚拟容器只用于只读索引，不执行预处理。
        clean=re.sub(r'<\?xml\b[^>]*\?>','',raw)
        try:
            root=ET.fromstring('<audit-root>'+clean+'</audit-root>')
        except ET.ParseError as error:
            raise ValueError('参考XML索引失败：'+path+'：'+str(error)) from error
        if path.endswith('/autoload_configs/modules.conf.xml'):
            loaded=[node.attrib['module'] for node in root.iter('load') if 'module' in node.attrib]
        for config in root.iter('configuration'):
            name=config.get('name','').removesuffix('.conf')
            if name:
                config_files['mod_'+name].add(path)
        if '/sip_profiles/' in path:
            config_files['mod_sofia'].add(path)
        if '/dialplan/' in path:
            config_files['mod_dialplan_xml'].add(path)
    return loaded,config_files


def runtime_inventory():
    """冻结自研源码和完整vendor依赖；宿主标识负向扫描只是辅助，不是形式化证明。"""
    files=set()
    for pattern in ['control/internal/**/*.go','control/cmd/**/*.go','control/go.mod','control/go.sum','control/vendor/**/*','media/src/**/*.rs','native/include/*.h','native/examples/*.c','native/audio/*.c','native/audio/*.py'] + ['control/**/*.' + suffix for suffix in GO_NATIVE_SUFFIXES]:
        files.update(path for path in ROOT.glob(pattern) if path.is_file() and not path.name.endswith('_test.go') and '/web/' not in str(path) and '/doc_assets/' not in str(path))
    inputs=[]
    matches=[]
    marker=re.compile(r'\b(?:SWITCH_MODULE_LOAD_FUNCTION|switch_loadable_module_load_module|switch_loadable_module_init)\b')
    for path in sorted(files):
        raw=path.read_bytes()
        relative=path.relative_to(ROOT).as_posix()
        inputs.append({'path':relative,'sha256':digest(raw),'bytes':len(raw)})
        for match in marker.finditer(uncomment(raw.decode('utf-8',errors='replace'))):
            matches.append({'path':relative,'symbol':match[0]})
    require(not matches,'出现原生宿主标识，须重新人工审查，不能沿用未实现结论：'+str(matches))
    return inputs


def build(source, tree_path):
    """组装逐目录、核心业务、扩展和成对验收矩阵，并在写文件前完成全部覆盖门禁。"""
    manifest,tree,sources,tree_modules=verify_sources(source,tree_path)
    catalog=json.loads((CATALOG/'source-catalog.json').read_text())
    require(catalog['reference_commit']==COMMIT,'接口目录不是固定提交')
    require(catalog['counts']['registration_sites']==538 and len(catalog['registrations'])==538,'538注册点分母变化')
    catalog_modules={item['module']:item for item in catalog['modules']}
    require(len(catalog_modules)==144,'原目录144项分母变化')
    with (CATALOG/'modules.csv').open(newline='',encoding='utf-8') as stream:
        csv_modules=list(csv.DictReader(stream))
    require(len(csv_modules)==144 and {item['module'] for item in csv_modules}==set(catalog_modules),'模块CSV和完整JSON目录不一致')
    union=set(catalog_modules)|set(tree_modules)
    require(set(MODULE_META)==union,'人工业务目录必须覆盖全树和旧清单并集')
    require(set(tree_modules)-set(catalog_modules)=={'mod_com_g729'},'全树新增候选变化，须人工审查')
    require(set(catalog_modules)-set(tree_modules)=={'autotools'},'SDK分母变化，须人工审查')
    declared=definitions(sources)
    require(sum(map(len,declared.values()))==144,'模块定义候选数变化，须人工审查')
    loaded,config_files=configured_modules(sources)
    evidence={key:dict({'id':key},**locate(*value)) for key,value in EVIDENCE.items()}
    registrations=defaultdict(list)
    for item in catalog['registrations']:
        registrations[item['module']].append(item)
        require(item['path'] in sources and 1<=item['line']<=len(sources[item['path']].splitlines()),'注册源定位失效')
    domains=[]
    for key,title,status,current,evidence_ids,paired in DOMAIN_ROWS:
        require(all(item in evidence for item in evidence_ids),'业务域引用了不存在的证据')
        domains.append({'id':key,'name':title,'status':status,'current_scope':current,'evidence_ids':evidence_ids,
                        'modules':sorted(name for name,value in MODULE_META.items() if value['domain']==key),
                        'paired_acceptance':{'id':'PAIR-'+key.upper().replace('_','-'),'status':'not_run','checks':paired}})
    known_domains={domain['id'] for domain in domains}
    entries=[]
    for name in sorted(union):
        meta=MODULE_META[name]
        require(meta['domain'] in known_domains,'模块业务域未定义：'+name)
        category=catalog_modules.get(name,tree_modules.get(name))['category']
        status='host_not_implemented'
        explanation='当前没有FreeSWITCH原模块宿主，未实现该模块的完整API、生命周期和副作用；原模块动态运行与成对差分均未执行。'
        ids=['native_abi','protocol_abi','media_commands']
        kind='source_module_candidate'
        if name in RELATIONS:
            status,explanation,ids=RELATIONS[name]
        if name=='autotools':
            status='sdk_example_only';kind='sdk_source_example'
            explanation='sdk/autotools定义mod_example用于开发示例，不能当作一个生产业务模块；原示例宿主ABI也未适配。'
        if name=='mod_com_g729':
            status='build_stub_only';kind='build_only_directory'
            explanation='完整固定Git树下仅列Makefile.am，选定C/C++清单因此没有该项；不能假定其实现、许可、构建产物或现场加载状态。'
        path=tree_modules[name]['path'] if name in tree_modules else 'src/mod/sdk/autotools'
        config=sorted(config_files.get(name,set()))
        entries.append({'id':'MODULE-'+name,'module':name,'category':category,'category_label':CATEGORIES[category],
                        'entry_kind':kind,'purpose':meta['purpose'],'domain':meta['domain'],
                        'source_path':path,'source_url':TREE_URL+path,'source_module_definitions':declared.get(name,[]),
                        'selected_source_file_count':catalog_modules.get(name,{}).get('source_files',0),
                        'in_original_catalog':name in catalog_modules,'in_complete_module_tree':name in tree_modules,
                        'status':status,'status_label':STATUS_LABELS[status],'current_scope':explanation,
                        'original_module_host':'not_implemented','original_binary_load_verified':False,
                        'freeswitch_runtime_loaded':'not_collected','paired_validation':'not_run','certification':'not_certified',
                        'rustswitch_evidence_ids':ids,'registration_count':len(registrations[name]),
                        'registration_ids':[item['id'] for item in registrations[name]],
                        'active_vanilla_load_declaration':name in loaded,
                        'configuration_artifact':{'status':'export_only' if config else 'not_in_mapped_vanilla_files','files':config,'runtime_executed':False},
                        'paired_case_ids':list(dict.fromkeys(['PAIR-NATIVE','PAIR-'+meta['domain'].upper().replace('_','-')])),
                        'required_runtime_followup':'冻结实际构建、启用模块及第三方依赖；比对load/unload、实际接口枚举、正常/错误/并发/重启及文件/事件副作用。'})
    validate_denominator(entries,union)
    mapped=[item for entry in entries for item in entry['registration_ids']]
    core=catalog['registrations']
    core=[item for item in core if item['module']=='core']
    require(len(core)==5,'内建注册点分母变化')
    all_ids=mapped+[item['id'] for item in core]
    require(len(all_ids)==len(set(all_ids))==538,'注册点遗漏或重复')
    require(set(all_ids)=={item['id'] for item in catalog['registrations']},'注册点集合未完整覆盖')
    runtime=runtime_inventory()
    test_tree=ast.parse((ROOT/'tests/e2e.py').read_text())
    test_names=sorted(node.name for node in ast.walk(test_tree) if isinstance(node,(ast.FunctionDef,ast.AsyncFunctionDef)) and node.name.startswith('test_'))
    module_statuses=Counter(entry['status'] for entry in entries if entry['in_complete_module_tree'])
    own=[
      {'id':'OWN-GUARD','name':'热更新业务准入策略','evidence_ids':['guard'],'scope':'并发、建立中与CPS保护；不计原limit模块兼容。'},
      {'id':'OWN-MEDIA','name':'Rust媒体分片与Linux批量收包','evidence_ids':['media_io','media_commands'],'scope':'内部实现和隔离手段；没有同场景FreeSWITCH基准，不能宣布更快或零故障。'},
      {'id':'OWN-ADMIN','name':'中文HTTP管理与文档界面','evidence_ids':['http_admin','xml_export'],'scope':'自有管理入口和配置产物，不能计为ESL/XML业务执行兼容。'},
      {'id':'OWN-TEST','name':'本机真实SIP/媒体测试编排','evidence_ids':['benchmark','callbench'],'scope':'有界独立实例、报告与清理；不替代独立发生器Linux容量或原版差分。'},
      {'id':'OWN-CODEC','name':'自有C codec ABI检查','evidence_ids':['codec_mode','codec_loader'],'scope':'可信G.711插件往返测试入口；不提供FreeSWITCH模块二进制兼容或通话转码。'}
    ]
    for item in own:
        item.update({'classification':'own_extension','source_implementation':'present','comparative_validation':'not_run','superiority_verified':False})
    input_paths=['docs/freeswitch-compatibility/catalog/source-catalog.json','docs/freeswitch-compatibility/catalog/modules.csv',
                 'docs/freeswitch-compatibility/catalog/reference-source-manifest.json','tests/e2e.py','tools/build_feature_audit.py']
    inputs=[{'path':path,'sha256':digest((ROOT/path).read_bytes())} for path in input_paths]
    return {'version':'1.0.0','audit_date':AUDIT_DATE,'scope':'固定源码树与当前自研实现的静态功能覆盖审计；不是运行兼容认证',
            'reference_version':VERSION,'reference_commit':COMMIT,'latest_release_check':LATEST,
            'verdict':{'drop_in_replacement':False,'complete_feature_equivalence':False,'full_superiority_proven':False,
                       'ten_thousand_media_capacity_certified':False,'freeswitch_dynamic_differential_executed':False,'certification':'not_certified',
                       'reason':'原模块宿主、完整ESL/XML/脚本、完整终端注册鉴权、安全媒体及多数业务模块未实现；没有成对完整业务与性能结果，不能无感替换或宣称全面超越。'},
            'denominators':{'original_catalog_entries':144,'original_catalog_entries_audited':144,'original_catalog_mod_prefixed_entries':143,
                            'complete_tree_mod_directories':144,'complete_tree_mod_directories_audited':144,'audit_union_entries':145,
                            'sdk_entries':1,'build_only_extra_entries':1,'selected_module_definition_sites':144,
                            'selected_source_files_sha256_and_git_blob_verified':len(manifest['files']),
                            'registration_sites':538,'module_registration_sites':len(mapped),'core_registration_sites':len(core),
                            'manual_registration_resolution':catalog['counts']['manual_registration_resolution'],
                            'verified_original_module_equivalence':0,'runtime_inventory_denominator_frozen':False},
            'inventory_findings':[{'id':'INVENTORY-SDK','description':'原catalog含sdk/autotools，实际声明mod_example；将其剔出mod_*目录分母但保留审计。'},
                                  {'id':'INVENTORY-BUILD-ONLY','description':'完整树额外包含mod_com_g729，仅有Makefile.am；已补入审计，未推断构建产物可用。'},
                                  {'id':'INVENTORY-CORE','description':'538个注册点包含5个core注册：MSRP相关4个和vpx 1个；已单列，不丢入模块目录统计。'},
                                  {'id':'INVENTORY-OUT-OF-TREE','description':'第三方、商业及现场自行编译模块不在固定src/mod树分母；真实二进制、启用模块、脚本、配置与依赖尚未采集。'}],
            'counts':{'module_statuses':dict(sorted(module_statuses.items())),'business_domains':len(domains),'own_extensions':len(own),
                      'registrations_by_kind':dict(sorted(Counter(item['kind'] for item in catalog['registrations']).items())),
                      'static_e2e_test_definitions':len(test_names),'freeswitch_paired_cases_executed':0},
            'vanilla_load_declarations':{'active_names':sorted(loaded),'outside_audited_module_directories':sorted(set(loaded)-union),'runtime_loaded':False},
            'evidence':list(evidence.values()),'modules':entries,
            'core_registrations':[dict(item, rustswitch_status='not_implemented',paired_validation='not_run') for item in core],
            'business_domains':domains,'own_extensions':own,
            'paired_acceptance_policy':{'all_cases_status':'not_run','required_cases':[domain['paired_acceptance']['id'] for domain in domains],
              'rules':['这些PAIR项是验收分组，不是全部行为用例的封闭总数；每个现场启用模块须展开完整正常、异常及副作用用例，冻结真正的强制用例分母。',
                       '冻结现场FreeSWITCH二进制、模块、依赖、配置、脚本和客户端哈希；无法采集的项保持未知。',
                       '同一输入在参考FreeSWITCH与RustSwitch各执行，收集协议字节、事件偏序、变量、CDR、文件、外部请求和资源副作用。',
                       '正常、边界、错误、并发、超时、重试、重启、负载和安全反例全部纳入；跳过、未跑和未知不得计为通过。',
                       '只允许预先冻结的归一化白名单，如随机UUID与时间偏移；错误码、腿归属、关键字段和因果顺序不得随意忽略。',
                       '容量比较须同硬件/内核/网卡/调频/NUMA及相同业务模块、编解码、媒体节奏和外部系统；发生器与被测机分离。',
                       '至少区分持续媒体、持续建拆、转码、录音、会议、全事件订阅、过载恢复及长稳；同时报告成功率、有效负载、逐向缺包、时延分位、CPU/RSS与RPO/RTO。',
                       'usage兼容、万路性能、drain切换、live在途迁移分别形成结论，任何一项不通过不能用别项替代。']},
            'static_validation':{'reference_hashes':'passed','complete_tree_not_truncated':'passed','catalog_and_tree_union':'passed',
                                 'unique_module_entries':'passed','registration_partition':'passed','source_locations':'passed',
                                 'known_host_marker_scan':'no_host_entry_found','runtime_tests':'not_run','e2e_definition_names':test_names},
            'inputs':inputs,'runtime_source_snapshot':runtime,
            'tree_snapshot':{'filename':tree_path.name,'sha256':digest(tree_path.read_bytes()),'commit':tree['sha'],'truncated':tree['truncated']}}


def markdown(audit):
    """生成可直接阅读的完整模块表，所有源码位置只使用已验证文件和官方固定提交链接。"""
    lines=['# FreeSWITCH 功能覆盖与超越能力审计','',
      '**结论：当前不能无感替换FreeSWITCH，不能宣称完整功能兼容，也没有证据证明全面超越或生产单机万路媒体容量。** 本文逐项覆盖固定目录与关键业务域；“覆盖完整”仅指静态审计分母闭合，不是功能实现率。','',
      '审计日期：'+AUDIT_DATE+'。固定参考：FreeSWITCH '+VERSION+'，提交 `'+COMMIT+'`。自研运行源码哈希随JSON一并冻结；原版动态差分本轮执行次数为0。','',
      '## 版本与范围','',
      '本次实际打开官方[最新发行入口]('+LATEST['source_url']+')，跳转到[FreeSWITCH v1.11.3]('+LATEST['resolved_url']+')，页面标为Latest，和本项目固定基线一致。页面列出该版的安全、稳定性修复及Sofia/Verto聊天API协议门禁变化；不能把旧分支或master的行为混入本基线。[固定提交](https://github.com/signalwire/freeswitch/commit/'+COMMIT+')用于源码定位。','',
      '这是有日期的正式发行观察；离线生成/检查不会自动重新确认未来的最新版本。用户现网二进制、发行补丁、构建选项、第三方模块和脚本尚未采集，不能把上游vanilla清单冒充现场完整运行清单。','',
      '## 找到的清单缺口与准确分母','',
      '| 分母 | 审计覆盖 | 含义 |','| --- | --- | --- |',
      '| 原源码目录catalog | 144 / 144 | 143个mod_*目录，加1个sdk/autotools示例目录 |',
      '| 完整固定Git树的mod_*目录 | 144 / 144 | 增补只有Makefile.am的mod_com_g729；仍不是已构建/加载模块数 |',
      '| 本审计并集 | 145 / 145 | 保留所有原条目、SDK示例及新增构建目录 |',
      '| API/应用等注册声明位置 | 538 / 538 | 533个映射到目录，5个内建core注册；重复与条件编译声明仍保留 |',
      '| 所选参考源文件 | 712 / 712 | 同时复核SHA-256及完整树中的Git blob SHA-1 |',
      '| 已认证原模块等价 | 0 | 没有原模块宿主，没有FreeSWITCH成对动态验收结果 |','',
      '`sdk/autotools`源码声明的是`mod_example`；SDK示例不能计为生产业务模块。完整树的[`mod_com_g729`]('+TREE_URL+'src/mod/codecs/mod_com_g729)仅列构建文件，C/C++选定文件扫描没有收录它。此次补齐的是目录审计边界，不推断该产物可以构建、下载、授权或加载。','',
      '538是注册宏出现位置，其中292个API、222个应用、10个JSON API和14个聊天应用；17个声明仍需人工解析，不能视作全部独立运行命令数。内建MSRP和vpx注册另列；宏动态生成、实际模块加载、外部原生模块、数据库/脚本后端及现场配置决定的能力，仍需运行盘点。原catalog的875个param出现位置与管理台4,304个可编辑属性使用不同计数规则，均不等于运行功能数。','',
      '## 当前确实提供的有限范围','',
      '- 自研Go SIP UDP/TCP/TLS可信固定中继，受限INVITE/ACK/CANCEL/BYE/OPTIONS、双腿状态、早期媒体与释放流程；并非完整Sofia端点。',
      '- 控制面新增PCMU/PCMA、G722、Opus、G729、G726/AAL2各码率及L16格式协商、独立RTP时钟与CN描述；Rust媒体同编码转发、分片和Linux批量接收必须按实际接线及逐格式结果验证，不能将协商/测试向量等同转码、混音、录音或WebRTC。',
      '- 自有HTTP管理、热更新准入、JSONL事件、配置草稿与真实本机测试工具；均有自己的协议与数据模型。',
      '- 官方XML编辑和ZIP导出，以及自有C codec ABI检查；XML不进入当前业务引擎，插件检查不等于FreeSWITCH模块或真实转码通话。','',
      '下表“相近用途”“部分协议”“仅导出”均不计原模块兼容分子。源码实现、项目自测、原版成对验收是三种不同证据；本审计执行的是静态检查，既有回归定义不算本轮已跑。','',
      '## 关键业务域逐项结论','',
      '| 业务域 | 当前判断 | 成对验收仍需执行 |','| --- | --- | --- |']
    for domain in audit['business_domains']:
        lines.append('| '+domain['name']+' | '+domain['current_scope']+' | `'+domain['paired_acceptance']['id']+'`：'+domain['paired_acceptance']['checks']+' |')
    lines += ['', '## 完整目录审计表', '',
              '每一行均已定位固定源码；“原模块宿主未实现”不是动态测试失败，而是当前没有可执行原模块及宿主语义的实现。此静态审计不采集原版加载或运行结果；已执行的限定成对结果请查看独立成对报告。已在vanilla配置中声明load也不代表本机曾构建或加载。','',
              '| 模块 / 目录 | 分类 | 原功能范围 | RustSwitch现状 | 注册点 | 固定来源 |','| --- | --- | --- | --- | --- | --- |']
    for entry in audit['modules']:
        declarations=entry['source_module_definitions']
        url=declarations[0]['source_url'] if declarations else entry['source_url']
        lines.append('| `'+entry['module']+'` | '+entry['category_label']+' | '+entry['purpose']+' | '+entry['status_label']+' | '+str(entry['registration_count'])+' | [源码]('+url+') |')
    lines += ['', '每行完整JSON还包含：与vanilla配置文件的对应关系、原加载声明、所有注册点ID、宿主状态、功能重叠的严格范围、RustSwitch证据以及必需成对用例。没有配置文件映射的模块仍可用高级XML编辑器创建产物，但不因此获得运行能力。','',
              '### 内建注册点','', '| 命令/应用 | 源码 | 状态 |','| --- | --- | --- |']
    for item in audit['core_registrations']:
        lines.append('| `'+str(item['name'])+'`（'+item['kind']+'） | [固定源码]('+item['source_url']+') | 未实现；原版差分未跑 |')
    lines += ['', '## 自有扩展不能作为全面超越的证据','', '| 实现 | 准确范围 |','| --- | --- |']
    for item in audit['own_extensions']:
        lines.append('| '+item['name']+' | '+item['scope']+' |')
    lines += ['', '目前能够说明架构选择和特定实现存在，不能说明FreeSWITCH没有类似能力，更不能把Rust语言、减少进程内共享、管理界面或单次低负载测试当作全面性能优势。完整功能集仍明显不足；功能等价未达到之前，同一编解码转发基准也不能外推录音、转码、会议与全部事件业务。','',
              '已有历史容量结果的边界见[0.2优化报告](../optimization-v0.2.md)与[可靠性审查](../optimization-review-2026-09-05.md)：曾建立万路不代表万路持续双向有效负载通过。当前Linux生产服务器验收尚未完成，跨机在途状态接管和零服务损失也未证明。','',
              '## 必需的成对验收规则','']
    for index,rule in enumerate(audit['paired_acceptance_policy']['rules'],1):
        lines.append(str(index)+'. '+rule)
    lines += ['', '所有上表`PAIR-*`用例目前为`not_run`。实际认证还需使用[冻结基线模板](../freeswitch-compatibility/templates/baseline-profile.json)、[接口合同](../freeswitch-compatibility/templates/interface-contract.json)和[认证报告](../freeswitch-compatibility/templates/certification-report.json)，以及[既有验收标准](../freeswitch-compatibility/06-conformance-and-cutover.md)。未知、未跑、失败、跳过都不能进入通过分子。','',
              '## 证据定位','', '| 证据ID | 当前自研文件位置 | 审查含义 |','| --- | --- | --- |']
    for item in audit['evidence']:
        lines.append('| `'+item['id']+'` | `'+item['path']+':'+str(item['line'])+'` | '+item['description']+' |')
    lines += ['', '以上本地行号与文件SHA-256收录在[完整审计JSON](feature-audit.json)。源码引用是定位依据，不是未提供的远程链接。全量目录原始数据来自[固定源码catalog](../freeswitch-compatibility/catalog/source-catalog.json)。','',
              '## 可重复静态校验','',
              '在项目根目录执行；默认参考目录为工作区已保存的固定源码及完整Git树，也可用`--source`和`--tree`显式指定。生成器只更新本审计的Markdown/JSON。','',
              '```sh','python3 tools/build_feature_audit.py','python3 tools/build_feature_audit.py --check --self-test','```','',
              '`--check`逐文件核验参考SHA-256/Git blob、完整树未截断、145条并集唯一覆盖、538注册点分区、证据行号与所有当前运行源码哈希，并验证已提交文档与重新生成结果一致。`--self-test`确认重复/遗漏条目与注释伪模块会被检测。失败会返回非零，不会静默修改文档。','',
              '默认校验不联网、不编译FreeSWITCH、不启动服务、不发媒体，也不运行FS动态差分。`--verify-latest`可选通过官方GitHub最新发行API只读复核；发现版本变更就失败，要求人工更新冻结基线，不自动把移动版本当作已审计版本。','',
              '本轮实际完成以上静态生成和复验，定义枚举到'+str(audit['counts']['static_e2e_test_definitions'])+'个项目e2e用例，但没有把用例定义冒充本轮运行。所有动态兼容、生产容量、drain/live切换与超越结论继续保持未认证。','']
    return '\n'.join(lines)


def self_test():
    """用独立内存反例检验分母门禁和注释过滤，不修改真实源码或启动任何服务。"""
    validate_denominator([{'module':'a'},{'module':'b'}],{'a','b'})
    rejected=0
    for entries in [[{'module':'a'}],[{'module':'a'},{'module':'a'}],[{'module':'a'},{'module':'c'}]]:
        try:
            validate_denominator(entries,{'a','b'})
        except ValueError:
            rejected+=1
    require(rejected==3,'负向分母用例未全部失败')
    sample='/* SWITCH_MODULE_DEFINITION(mod_fake,0,0,0); */\nSWITCH_MODULE_DEFINITION(mod_real,0,0,0);'
    names=re.findall(r'SWITCH_MODULE_DEFINITION\s*\(\s*(mod_\w+)',uncomment(sample))
    require(names==['mod_real'],'模块注释伪声明未排除')
    return 5


def main():
    """命令行仅生成两个审计产物；检查模式只比较现有文件，联网核对必须显式选择。"""
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source',type=Path,default=ROOT.parents[1]/'work/freeswitch-reference/freeswitch-1.11.3')
    parser.add_argument('--tree',type=Path,default=ROOT.parents[1]/'work/freeswitch-reference/tree-v1.11.3-complete.json')
    parser.add_argument('--check',action='store_true',help='只复验覆盖、输入及审计文件一致性，不写文件')
    parser.add_argument('--self-test',action='store_true',help='执行静态分母与注释过滤反例')
    parser.add_argument('--verify-latest',action='store_true',help='显式联网只读检查官方最新正式发行，变更时失败')
    args=parser.parse_args()
    if args.verify_latest:
        request=urllib.request.Request('https://api.github.com/repos/signalwire/freeswitch/releases/latest',headers={'User-Agent':'RustSwitch-feature-audit'})
        with urllib.request.urlopen(request,timeout=20) as response:
            latest=json.load(response)
        require(latest['tag_name']=='v'+VERSION and not latest['draft'] and not latest['prerelease'],'最新正式发行已变化；需重新冻结基线，不能沿用当前审计结论')
    audit=build(args.source,args.tree)
    checks=self_test() if args.self_test else 0
    outputs={'feature-audit.json':json.dumps(audit,ensure_ascii=False,indent=2)+'\n','feature-audit.md':markdown(audit)}
    for filename,text in outputs.items():
        path=ROOT/'docs/api'/filename
        if args.check:
            require(path.exists() and path.read_text()==text,'审计已过期，请重新生成：'+filename)
        else:
            path.write_text(text,encoding='utf-8')
    print(json.dumps({'passed':True,'mode':'check' if args.check else 'generate','catalog_coverage':'144/144','complete_tree_module_coverage':'144/144',
                      'union_entries':145,'registration_sites':538,'reference_files_verified':712,'negative_and_smoke_checks':checks,
                      'runtime_tests_executed':0,'certification':'not_certified'},ensure_ascii=False))


if __name__=='__main__':
    try:
        main()
    except (ValueError,OSError,KeyError,ET.ParseError) as error:
        print('审计失败：'+str(error),file=sys.stderr)
        raise SystemExit(1)
