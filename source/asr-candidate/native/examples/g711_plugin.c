/* 用于验证 RustSwitch 自定义 ABI 的 PCMU 示例插件。
 * 仅进行 8 kHz 单声道样本与 G.711 μ-law 字节转换，不是 FreeSWITCH 原生模块，
 * 也不实现 SIP 协商、RTP 封包、线程隔离或实际通话中的转码调度。
 */
#include "rustswitch_codec.h"
#include <stdlib.h>

/* 把一个有符号 PCM 样本压扩为 μ-law。使用 int 中间值以安全处理 int16_t 最小值，
 * 再执行幅度裁剪、偏置和分段量化；不访问外部状态。 */
static uint8_t to_mulaw(int16_t sample) {
    static const int ends[8] = {0xff,0x1ff,0x3ff,0x7ff,0xfff,0x1fff,0x3fff,0x7fff};
    int value=sample, mask=0xff, segment=0;
    if (value<0) {value=-value;mask=0x7f;}
    if (value>32635) value=32635;
    value+=132;
    while (segment<8 && value>ends[segment]) ++segment;
    return (uint8_t)(((segment<<4)|((value>>(segment+3))&15))^mask);
}
/* 对一个 μ-law 字节解压扩，恢复符号和量化后的 PCM 幅值；结果存在有损量化误差。 */
static int16_t from_mulaw(uint8_t value) {
    value=(uint8_t)~value;
    int sample=(((value&15)<<3)+132)<<((value>>4)&7);
    return (int16_t)((value&128)?132-sample:sample-132);
}
/* 示例没有实际 codec 状态，仍分配一个非空标记，验证宿主 create/destroy 生命周期。
 * 无效输出指针返回 -1，分配失败返回 -2；成功上下文必须交由 destroy 释放。 */
static int32_t create(void **context) {
    if (!context) return -1;
    *context=malloc(1);
    return *context?0:-2;
}
/* 由宿主在最后一次回调后调用；上下文不共享，不能重复释放。 */
static void destroy(void *context) {free(context);}
/* 一个样本生成一个编码字节。先检查所有指针、输出容量和示例帧上限，
 * 失败时不报告输出长度；输入输出仅在本次同步调用期间使用。 */
static int32_t encode(void *context,const int16_t *input,size_t count,uint8_t *output,size_t capacity,size_t *length) {
    if (!context||!input||!output||!length||count>capacity||count>1600) return -1;
    for(size_t i=0;i<count;++i) output[i]=to_mulaw(input[i]);
    *length=count;
    return 0;
}
/* 一个编码字节生成一个 PCM 样本；capacity/length 均以样本数计而非字节数。 */
static int32_t decode(void *context,const uint8_t *input,size_t count,int16_t *output,size_t capacity,size_t *length) {
    if (!context||!input||!output||!length||count>capacity||count>1600) return -1;
    for(size_t i=0;i<count;++i) output[i]=from_mulaw(input[i]);
    *length=count;
    return 0;
}
/* 静态只读描述符随动态库一直存活；1600 是单次缓冲上限，不等于协商的 RTP 帧长。 */
static const rs_codec_v1 codec={RS_CODEC_ABI_VERSION,sizeof(rs_codec_v1),"PCMU",8000,1,1600,1600,create,destroy,encode,decode};
/* 精确导出约定入口，返回借用地址；宿主不能释放描述符或在回调存活时卸载动态库。 */
const rs_codec_v1 *rs_codec_get_v1(void) {return &codec;}
