/* 协议适配器预留契约，尚未实现 Sofia-SIP/PJSIP 提供器，也不是 FreeSWITCH 模块 ABI。
 * 此头仅固定函数边界；JSON 消息 schema、错误恢复和版本协商仍需补齐并验证。
 */
#ifndef RUSTSWITCH_PROTOCOL_H
#define RUSTSWITCH_PROTOCOL_H
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
/* C++ 实现使用 C 链接约定，并自行捕获异常，不能跨越 ABI 传播栈展开。 */
extern "C" {
#endif
/* 为独立协议提供器预留的适配接口。
 * JSON 事件与命令必须符合另行约定的提供器 schema；不能凭此头推断消息已兼容。
 * poll_event 返回 1 表示产出事件，0 表示超时，负值表示错误。
 * 提供器拥有 SIP 事务、重传定时器与传输 socket；Go 控制面拥有路由和跨腿业务状态。
 * 一条事务只能由一个提供器负责，不能让两个状态机同时执行重传或终止。
 */
typedef struct rs_protocol_v1 {
    /* 协议适配 ABI 版本与描述符大小；实际装载入口尚待实现。 */
    uint32_t abi_version;
    uint32_t struct_size;
    /* 配置字节仅借用到调用返回；提供器拥有创建的上下文，最终由 destroy 释放。 */
    int32_t (*create)(const uint8_t *config, size_t config_len, void **context);
    /* 提交带显式长度的命令，不能依赖输入以 NUL 结尾；异步保存时须自行复制。 */
    int32_t (*submit)(void *context, const uint8_t *command, size_t command_len);
    /* 在 timeout_ms 内尝试取得一个事件；写出长度不得超过 capacity。 */
    int32_t (*poll_event)(void *context, uint8_t *output, size_t capacity, size_t *length, uint32_t timeout_ms);
    /* 结束提供器生命周期，释放其事务、传输和私有存储；并发调用规则尚须 schema 配套约定。 */
    void (*destroy)(void *context);
} rs_protocol_v1;
#ifdef __cplusplus
}
#endif
#endif
