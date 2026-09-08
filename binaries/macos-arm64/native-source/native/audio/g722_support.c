/* G.722 独立构建桥接：仅实现标准堆分配及循环点积，编解码算法保持官方源码原样。
 * 点积的 12 个滤波抽头由 G.722 固定调用；以 int64_t 累加避免中间有符号溢出。
 * 此文件为 RustSwitch 自研，不包含 SpanDSP 其他模块或全局分配器扩展。 */
#include <stdint.h>
#include <stdlib.h>
void *span_alloc(size_t size) { return malloc(size); }
void span_free(void *pointer) { free(pointer); }
int32_t vec_circular_dot_prodi16(const int16_t *x,const int16_t *y,int n,int pos) {
    int64_t sum=0;
    for(int i=0;i<n;i++) sum+=(int64_t)x[(pos+i)%n]*y[i];
    return (int32_t)sum;
}
