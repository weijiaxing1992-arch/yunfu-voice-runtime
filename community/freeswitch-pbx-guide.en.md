# FreeSWITCH, PBX and RustSwitch: a practical guide to telephony and Voice Agents

[中文](freeswitch-pbx-guide.md) · [English](freeswitch-pbx-guide.en.md) · [Project home](../README_EN.md) · [Documentation](../DOCUMENTATION.md)

Updated **2026-09-09**. This guide is for developers, integrators and operators evaluating open-source PBX, IP PBX, SIP platforms and telephone Voice Agents. It explains the vocabulary, configuration boundaries, test questions and actual RustSwitch scope.

Upstream descriptions link to SignalWire's official FreeSWITCH documentation. RustSwitch statements refer to main interface baseline **1.13.0**, the [protocol contracts](../05-协议与SDK文档.md) and [remaining-work register](../13-项目未完成清单.md). Our compatibility reference remains **FreeSWITCH 1.11.3**; continuously updated upstream documentation is not evidence that this pinned version was executed in a paired test.

> **RustSwitch is a development-preview FreeSWITCH alternative focused on Voice Agents.** PBX capabilities described here are not automatically implemented in RustSwitch. A complete PBX, full protocol compatibility, real ASR–LLM–TTS integration and complete 5,000/10,000-call acceptance remain unfinished.

## 1. Start with the telephone service you need

A PBX, or Private Branch Exchange, serves an organization's internal and external telephone communication; an IP PBX uses IP telephony for that work. Evaluation usually includes extensions, routing, permissions, attendants, transfers, messages and operations. FreeSWITCH is a programmable communications switching platform whose default configuration provides a SIP PBX starting point. [Official core concepts](https://developer.signalwire.com/freeswitch/foundations/introduction/)

Start with required call flows rather than a protocol count:

| Workload | Define first | RustSwitch evaluation focus |
|---|---|---|
| Office telephony | Extension registration, dialing permissions, transfers, voicemail, device management | Complete extension PBX behavior is unfinished; inspect gaps first |
| Customer service / contact center | Queues, agent state, human handoff, recordings, call records | Admission throttling does not implement an agent queue |
| Automated menus | Answer, prompts, digits, timeouts, branches, hangup | Limited local IVR/read/playback can be tested individually |
| Telephone Voice Agent | Continuous receive audio, streaming speech, turn cancellation, model timeouts | G.711/PCM/RX foundations exist; real model integration is pending |
| SIP trunk media service | Signaling, codecs, source validation, resource budgets | Begin with a fixed trunk and a defined same-format media path |

These are evaluation steps derived from this project's scope, not a vendor ranking or purchasing recommendation. Keep existing service in place when it supplies a mandatory capability that this preview lacks, and evaluate the candidate path in isolation.

## 2. PBX, media server and SBC are different responsibilities

One product may combine several roles. Acceptance should still observe them separately:

| Role | Responsibility to evaluate | RustSwitch boundary |
|---|---|---|
| PBX / IP PBX | Who may call, where numbers route, how extension features behave | Selected trunk and application features, not a complete enterprise phone suite |
| SIP control plane | Dialog setup/teardown, authentication, routing, transactions and events | Restricted SIP and call-state management in Go |
| Media server | Audio transport, playback, digits, transcoding, mixing or recording | Rust relay, G.711 real-time graph and PCM/RX foundations |
| [SBC / session border controller](https://signalwire.com/blog/metaswitch-end-of-life) | Signaling, media and security policy at network boundaries | Two-leg bridging, allowlists and rate limits do not establish full SBC capability |
| Voice Agent application | Recognition, business decisions, generated speech and conversation turns | Real suppliers and complete application orchestration remain to be integrated |

FreeSWITCH distinguishes processing media, proxying media through the server and bypassing the server's media path. Consequently, a connected-call count alone does not establish the amount of audio work performed. [Official media handling](https://developer.signalwire.com/freeswitch/media-and-codecs/handling/)

This is a **responsibility sketch**, not an already integrated preview deployment:

```text
Phone / softphone / carrier SIP trunk
                   |
       Network and security boundary
                   |
      Call routing and state control -- Operations and events
                   |
          RTP and audio processing
                   |
       ASR -> Business / LLM -> TTS
```

Actual RustSwitch processes, ports and candidate paths are documented in [deployment topology](../02-项目拓扑图.md) and [software architecture](../03-软件架构图.md). External AI boxes do not supply a model account, a hosted service or billing integration.

## 3. SIP trunks, upstream registration and extension registration

A SIP trunk is a logical calling connection to a carrier or another telephone system. FreeSWITCH gateways belong to Sofia profiles and may register upstream or use a peer connection without REGISTER. The carrier's interconnect contract determines which is required. [Official gateways and registration](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/)

**Registering to an upstream** makes this service a client. **Accepting phone registrations** requires authenticating endpoints, maintaining contact bindings and routing calls to them. FreeSWITCH's Sofia profiles and user directory participate in those functions. [Official SIP profiles](https://developer.signalwire.com/freeswitch/users-and-endpoints/sip-profiles/)

RustSwitch currently has one fixed-upstream REGISTER/Digest client. The network destination comes from `sip.upstream`; this is neither full multi-gateway management nor a registrar for softphones. Restart requirements, URI restrictions and private credential-file rules are documented in the [RustSwitch registration contract](../reference/docs/api/trunk-registration.md).

For a real trunk, capture at least these questions:

| Area | Test question |
|---|---|
| Registration and call authentication | Does INVITE require a separate challenge even after REGISTER succeeds? |
| Identity and routing | Are the authentication user, address of record, caller ID and destination format independently correct? |
| Reachability | Can the peer reach Contact, advertised signaling and advertised media addresses? |
| Refresh and failure | Do expiration, rejected credentials, transport loss and timeouts recover without unbounded retries? |
| Direction | Have inbound and outbound calls both been tested through actual endpoints? |
| Teardown | Do CANCEL, BYE, missing ACK and transport failure release the correct resources? |

Successful registration establishes a registration result. It does not by itself prove destination routing, caller-ID permission, bidirectional speech or sustained availability.

## 4. How IVR, DTMF, ESL and XML fit together

### IVR: a prompt is only the beginning

Interactive Voice Response connects prompts, caller input and branch actions. FreeSWITCH provides XML menus invoked through the `ivr` application, including menus, submenus and actions. [Official IVR menus](https://developer.signalwire.com/freeswitch/applications/ivr-menus/)

RustSwitch has limited local playback, `read` digit collection and selected basic applications. Test initial and inter-digit timeouts, terminators, repeated input, prompt interruption, playback failure, hangup cancellation and result variables. Successful `read` does not establish a complete menu-tree implementation. [RustSwitch IVR contract](../reference/docs/api/ivr-reference.md)

### DTMF: test digits separately from speech audio

Common digit paths are RTP `telephone-event`, SIP INFO and audible in-band tones. FreeSWITCH retains the `rfc2833` configuration name; its glossary identifies RFC 4733 as the successor. RTP telephone events are distinct from detecting tones inside speech audio. [Official glossary](https://developer.signalwire.com/freeswitch/reference/glossary/)

RustSwitch implements contract-limited RTP event receive/local send and explicitly enabled INFO digit collection. That does not provide in-band detection or all INFO formats. Locally submitted DTMF packets do not prove that the remote IVR accepted a digit. [DTMF sending contract](../reference/docs/api/dtmf-send-reference.md)

### ESL: separate command responses from events

FreeSWITCH's `mod_event_socket` exposes control and events over TCP. In inbound mode an external process connects to FreeSWITCH; in outbound mode FreeSWITCH connects to an external process for a call. [Official Event Socket](https://developer.signalwire.com/freeswitch/integration/event-socket/)

RustSwitch has an explicitly enabled inbound ESL subset. Clients must handle authentication, byte lengths, fragmented/combined frames and events interleaved with responses. `+OK` confirms the documented acceptance stage; playback or digit completion still needs correlated events, result fields and real media observations. [RustSwitch ESL contract](../reference/docs/api/esl-reference.md)

### XML: editable configuration is not executable capability

The FreeSWITCH XML dialplan organizes routing and applications with contexts, extensions, conditions and actions. Variable handling and evaluation order are part of its behavior. [Official XML dialplan](https://developer.signalwire.com/freeswitch/dialplan/xml/)

RustSwitch's configuration workspace, XML export and startup-frozen runtime dialplan subset are separate capabilities. Exporting conference or voicemail configuration does not run the corresponding module. [RustSwitch dialplan contract](../reference/docs/api/dialplan-reference.md)

## 5. PBX feature map: upstream functionality and this preview

The table places official upstream entry points beside RustSwitch's actual boundary. It is a reading index, not an interoperability certificate. Current details come from the [capability status](../08-当前能力与验证状态.md) and [work register](../13-项目未完成清单.md).

| PBX feature | Official FreeSWITCH entry point | RustSwitch scope / gap |
|---|---|---|
| Extensions and user directory | [Configuration relationships](https://developer.signalwire.com/freeswitch/foundations/introduction/) | Limited exact-number local answering; full endpoint registration, directory and tenancy are unfinished |
| Trunks, gateways and registration | [Sofia gateways](https://developer.signalwire.com/freeswitch/users-and-endpoints/gateways/) | One fixed-upstream client; multiple trunks and complete gateway behavior are unfinished |
| IVR, prompts and digits | [IVR menus](https://developer.signalwire.com/freeswitch/applications/ivr-menus/) | Limited local implementation; full menus, scripts and parameters need further work |
| Blind and attended transfers | [`transfer` / `att_xfer`](https://developer.signalwire.com/freeswitch/dialplan/dptools/) | Bridging or `park` does not establish complete transfer behavior; REFER remains a gap |
| Call recording | [`record_session` / `uuid_record`](https://developer.signalwire.com/freeswitch/media-and-codecs/audio-files/) | Receive-audio access is not a recording product; complete recording and dual-track storage remain open |
| Conferencing and mixing | [`mod_conference`](https://developer.signalwire.com/freeswitch/applications/conferencing/) | Complete conferencing is unfinished; two-leg bridging is not multiparty mixing |
| Queues, agents and ACD | [`mod_fifo` / `mod_callcenter`](https://developer.signalwire.com/freeswitch/applications/call-queues/) | Full queues, agent assignment and agent-state management are unfinished |
| Voicemail | [`mod_voicemail`](https://developer.signalwire.com/freeswitch/applications/voicemail/) | Complete message deposit, retrieval and management are unfinished |
| External events and control | [`mod_event_socket`](https://developer.signalwire.com/freeswitch/integration/event-socket/) | Limited inbound commands/events; outbound ESL and complete event semantics are unfinished |
| Call detail records | [Event, CDR and logging modules](https://developer.signalwire.com/freeswitch/module-reference/event-handlers/) | Local event logs do not implement complete billing records, reconciliation or upstream CDR modules |
| Browser calling | [SIP over WSS](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/) | The admin console is not a WebRTC phone; WS/WSS, ICE and DTLS-SRTP remain open |

Conferences, queues and voicemail each own additional state and resources. In particular, FreeSWITCH's queue/agent/tier callcenter model cannot be replaced by a limit on calls being established. [Official queue model](https://developer.signalwire.com/freeswitch/applications/call-queues/)

## 6. What Voice Agents add to PBX acceptance

A PBX call flow is a starting point. Voice Agents also connect media to asynchronous external services. The following are this project's integration test recommendations:

- **Continuous input:** track audio origin, session/generation and sample rate; missing or expired frames must not masquerade as real user silence.
- **Streaming output:** late TTS, excessive queueing or cancelled turns must not inject old speech into a later turn.
- **Interruption:** distinguish business cancellation, queued-data cancellation, media stop and remaining buffers.
- **Model failure:** ASR/TTS timeouts, disconnected suppliers and slow consumers need explicit cleanup or a defined degraded outcome.
- **Human handoff:** test the actual connection/transfer path and its fallback; a diagram does not implement a contact center.
- **Evidence:** correlate audio, text, application events and channel identity, while sanitizing public reports.

The main project provides G.711, PCM downlink and receive-audio foundations. ASR1 is a separately published candidate not merged into the main project, and its mock validates protocol behavior. Real recognition, TTS, LLM, automatic VAD interruption and full application acceptance remain open. [Audio and candidate contracts](../05-协议与SDK文档.md)

## 7. Codecs and WebRTC: inspect the actual media path

G.711 PCMA/PCMU, G.722, Opus or G.729 appearing in SDP or an administration page may indicate parsing, negotiation, relay or real codec execution. State which level a deployment needs instead of asking only whether a codec name is supported.

RustSwitch's current real-time graph is based on G.711. Optional G.722/Opus libraries and offline checks do not mean those codecs are wired into every real-time path. G.729 and G.726 relay should be evaluated against their payload contracts, not described as complete live transcoding. [RustSwitch audio capabilities](../reference/docs/api/audio-reference.md)

FreeSWITCH browser calling includes SIP over WS/WSS and Verto signaling paths; browser media also needs ICE and DTLS-SRTP. A SIP TCP/TLS listener or a web button does not supply that browser protocol stack. [Official WebRTC over SIP](https://developer.signalwire.com/freeswitch/users-and-endpoints/webrtc-sip/)

RustSwitch's administration “small phone” test is currently an isolated SIP/media simulation. Browser microphone access, mobile networks and NAT traversal require separate protocol and security acceptance; simulation results do not establish them.

## 8. A reproducible PBX migration evaluation

1. **Pin versions:** record FreeSWITCH source/configuration, RustSwitch commit/variant, toolchains and binary hashes.
2. **List business flows:** separate registration, inbound/outbound calls, IVR, transfer, recording and event consumption.
3. **Map dependencies:** identify actual commands, variables, modules, events and media formats; compare them with the backlog.
4. **Start with one call:** retain both sides' SIP/SDP, events, bidirectional audio and final resource state.
5. **Add failures:** rejected authentication, CANCEL/answer races, missing ACK, failed playback, timeouts and worker restart.
6. **Then test capacity:** hold codecs, packetization, CPS, duration, recording/AI features and hardware conditions constant.
7. **Publish a scoped result:** describe passed scenarios, differences, unrun cases and rollback needs without inventing an overall percentage.

The [3,999 original comparison records](../backlog/FreeSWITCH-全部逐项状态.csv) mix declarations, configuration and acceptance definitions; they are not 3,999 independently implemented PBX features. Historical green results do not certify today's source, and failed gates or capacity runs must remain visible. [Evidence rules](../DOCUMENTATION.md)

Load reports should separate call attempts, observed established-call peak, offered media, valid received media, errors/loss, tail latency and resources remaining after teardown. RustSwitch tooling is also constrained by isolated ports and generator capacity. Incomplete results cannot pass acceptance. [Operations and load testing](../07-运维与压测说明.md)

## 9. Frequently asked questions

### Can RustSwitch replace a complete FreeSWITCH PBX now?

No such conclusion is supported. Limited trunk, media, IVR and ESL/XML paths exist, while full extensions, transfers, conferencing, queues, voicemail and other modules remain open. Test the deployment's actual requirements rather than replacing every existing feature at once.

### Registration succeeds, but calls fail. What should I inspect first?

Separate registration state, arriving INVITE, routing/authentication, SDP addressing/codecs, actual ACK and bidirectional RTP. Preserve a correlated transaction or channel identity for each step. Registration state alone cannot locate an audio failure.

### Does `+OK`, HTTP 200 or `uuid_exists=true` prove the business action completed?

No. Distinguish command acceptance, application completion, actual media processing, peer receipt and the business outcome. A complete call also requires hangup and cleanup. See [ESL](../reference/docs/api/esl-reference.md) and [event lifecycle](../reference/docs/api/event-lifecycle-reference.md).

### How do call queues differ from overload protection?

Queues hold callers for agents and assign them according to business rules. Admission protection controls new calls to reduce overload. RustSwitch's guard does not implement a complete ACD/agent queue or promise eventual connection through unlimited buffering.

### Why are voicemail and conference templates present if those features are unfinished?

Templates support editing/export. Runtime loading, application behavior, state, events, media and persistence are different implementation layers. Exporting upstream XML does not provide a FreeSWITCH module host.

### Do PCM/RX interfaces replace a recording or ASR product?

No. They are integration foundations. Storage/indexing, real recognition, failure recovery, access control and lifecycle acceptance still need implementation. The ASR mock does not recognize actual speech.

### What makes a useful compatibility contribution?

Submit an [interoperability report](https://github.com/weijiaxing1992-arch/yunfu-voice-runtime/issues/new?template=compatibility_report.yml) with pinned versions, the exact module/command, a minimal scenario, both results and sanitized evidence. Read [COMMUNITY](../COMMUNITY.md) and [CONTRIBUTING](../CONTRIBUTING.md) for the workflow.

## 10. Continue reading

- Assess the deployment: [45-domain PBX feature matrix](pbx-feature-matrix.md), [FreeSWITCH migration and rollback guide](freeswitch-migration.md) (Chinese).
- Install and experiment: [Quick start](../QUICKSTART.md), [user guide](../06-使用说明.md).
- Develop and integrate: [HTTP API](../04-HTTP接口文档.md), [protocols and SDKs](../05-协议与SDK文档.md), [source navigation](../source/README.md).
- Evaluate migration: [pinned comparison standard](../source/main/docs/freeswitch-compatibility/README.md), [current status](../08-当前能力与验证状态.md), [remaining work](../13-项目未完成清单.md).
- Redistribute safely: [license scope](../LICENSE_SCOPE.md), [third-party notices](../THIRD_PARTY_NOTICES.md), [security policy](../SECURITY.md).

This guide is an original RustSwitch community synthesis and set of integration recommendations. Official sources describe upstream capabilities; references to FreeSWITCH do not imply certification, endorsement or official distribution status. Availability of an upstream module still depends on the selected version's build, loading and configuration.
