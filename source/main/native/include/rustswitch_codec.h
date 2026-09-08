/* RustSwitch 自定义编解码插件 ABI；不兼容 FreeSWITCH 的 mod_*.so 二进制接口。
 * 描述符和回调由可信动态库提供，宿主只借用它们，不负责释放描述符本身。
 */
#ifndef RUSTSWITCH_CODEC_H
#define RUSTSWITCH_CODEC_H
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
/* 固定 C 符号链接方式，避免 C++ 名称修饰改变宿主查找的入口名。 */
extern "C" {
#endif

/* 当前描述符版本；任何不兼容布局变更都应使用新版本和相应入口。 */
#define RS_CODEC_ABI_VERSION 1u
/* 返回整型的回调以 0 表示成功，负值表示失败；destroy 不返回状态。
 * 一个上下文只归一个调用线程使用；不得保存输入/输出指针、阻塞媒体线程、
 * 抛出 C++ 异常或让其他语言的栈展开跨过此 ABI。
 * 输出长度只在成功时写入，且不得大于传入容量；编码容量以字节计，PCM 容量以样本计。
 * 所有上下文销毁前，宿主必须保持动态库已加载；检查器不能沙箱化不可信原生代码。
 */
typedef struct rs_codec_v1 {
    /* 必须与宿主理解的版本一致。 */
    uint32_t abi_version;
    /* 整个描述符的字节数，用于读取完整结构前检查布局。 */
    uint32_t struct_size;
    /* 固定大小名称字段，提供者应在可用空间内写入终止字符。 */
    char name[32];
    /* PCM 格式；样本类型固定为 int16_t。 */
    uint32_t sample_rate;
    uint32_t channels;
    /* 单次处理的样本总数与编码字节上限，用于宿主分配缓冲。 */
    uint32_t max_pcm_samples;
    uint32_t max_encoded_bytes;
    /* 成功时写出非空私有上下文，之后由 destroy 精确释放一次。 */
    int32_t (*create)(void **context);
    void (*destroy)(void *context);
    /* 同步 PCM→编码数据；所有指针仅在本次调用期间借用。 */
    int32_t (*encode)(void *context, const int16_t *input, size_t input_samples,
                      uint8_t *output, size_t output_capacity, size_t *output_length);
    /* 同步编码数据→PCM；output_capacity_samples 和 output_samples 均以样本数计。 */
    int32_t (*decode)(void *context, const uint8_t *input, size_t input_bytes,
                      int16_t *output, size_t output_capacity_samples, size_t *output_samples);
} rs_codec_v1;

/* 可信 C/C++ 动态库必须导出此精确符号；返回描述符至少活到动态库卸载。 */
const rs_codec_v1 *rs_codec_get_v1(void);
#ifdef __cplusplus
}
#endif
#endif
