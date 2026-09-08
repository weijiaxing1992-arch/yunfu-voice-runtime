//! 自定义 C 编解码 ABI 的独立连通性检查，不是实时转码器或 FreeSWITCH 模块加载器。
//! 原生代码必须可信；长度检查只能验证返回契约，不能隔离插件越界写或崩溃。
use anyhow::{ensure, Context, Result};
use libloading::Library;
use std::{
    ffi::{c_char, c_void},
    path::Path,
};

#[repr(C)]
/// 与 rustswitch_codec.h 按字段顺序一致的描述符；函数指针及上下文归插件所有。
struct CodecV1 {
    /// 自定义 ABI 版本，当前检查器只接受版本 1。
    abi_version: u32,
    /// 提供者声明的结构体字节数，在形成完整引用前检查。
    struct_size: u32,
    /// 插件名称的固定字节字段；检查器不将它当作任意长度 C 字符串读取。
    name: [c_char; 32],
    /// PCM 采样率和声道数；当前检查向量只覆盖 8 kHz 单声道。
    sample_rate: u32,
    channels: u32,
    /// 单次 PCM 样本容量和编码字节容量；长度单位分别为样本与字节。
    max_pcm_samples: u32,
    max_encoded_bytes: u32,
    /// 创建插件私有上下文；成功后由 destroy 精确释放一次。
    create: Option<unsafe extern "C" fn(*mut *mut c_void) -> i32>,
    destroy: Option<unsafe extern "C" fn(*mut c_void)>,
    /// 同步编码与解码回调，不得保留借入指针、跨 ABI 抛异常或写出输出容量。
    encode: Option<
        unsafe extern "C" fn(*mut c_void, *const i16, usize, *mut u8, usize, *mut usize) -> i32,
    >,
    decode: Option<
        unsafe extern "C" fn(*mut c_void, *const u8, usize, *mut i16, usize, *mut usize) -> i32,
    >,
}

/// 加载可信插件，验证描述符并执行一次 G.711 往返向量。
/// 动态库保留到上下文销毁之后；生产转码的独立故障隔离仍需另行实现和验收。
pub fn check(path: &Path) -> Result<()> {
    let path = path.canonicalize()?;
    // SAFETY: 安全依据：调用者显式选择可信本地库，并负责保证其实现约定的 C ABI；
    // 动态装载可能执行库初始化代码，因此本检查不是针对不可信插件的沙箱。
    let library = unsafe { Library::new(path) }.context("load codec shared library")?;
    // SAFETY: 安全依据：入口符号必须严格遵循 rustswitch_codec.h 中的函数签名；
    // library 句柄仍存活，因此查得的函数地址尚未被卸载。
    let descriptor = unsafe {
        let entry =
            library.get::<unsafe extern "C" fn() -> *const CodecV1>(b"rs_codec_get_v1\0")?;
        entry()
    };
    ensure!(!descriptor.is_null(), "null codec descriptor");
    // SAFETY: 安全依据：可信提供者须返回至少两个可读、正确对齐的 u32 字段；
    // 先检查这个前缀，避免在结构大小不匹配时直接形成完整 v1 引用。
    let (version, size) = unsafe {
        let prefix = descriptor.cast::<u32>();
        (prefix.read(), prefix.add(1).read())
    };
    ensure!(
        version == 1 && size as usize == std::mem::size_of::<CodecV1>(),
        "codec ABI version or struct size mismatch"
    );
    // SAFETY: 安全依据：提供者已声明完整、存活且对齐的 v1 描述符，版本与大小已核对；
    // 库句柄的生命周期覆盖此引用和所有回调调用。
    let codec = unsafe { &*descriptor };
    ensure!(
        (1..=48_000).contains(&codec.max_pcm_samples)
            && (1..=65_536).contains(&codec.max_encoded_bytes),
        "invalid codec buffer limits"
    );
    ensure!(
        codec.sample_rate == 8000 && codec.channels == 1,
        "this conformance vector requires 8 kHz mono"
    );
    let create = codec.create.context("missing create")?;
    let destroy = codec.destroy.context("missing destroy")?;
    let encode = codec.encode.context("missing encode")?;
    let decode = codec.decode.context("missing decode")?;
    let mut context = std::ptr::null_mut();
    ensure!(
        // SAFETY: 安全依据：context 指向可写的本地指针槽位；插件创建并拥有返回的上下文。
        unsafe { create(&mut context) } == 0 && !context.is_null(),
        "codec create failed"
    );
    /// 作用域守卫使后续任何验证错误都能释放已成功创建的上下文。
    struct Guard {
        context: *mut c_void,
        destroy: unsafe extern "C" fn(*mut c_void),
    }
    impl Drop for Guard {
        /// 析构顺序保证先调用插件销毁函数，再释放动态库句柄。
        fn drop(&mut self) {
            // SAFETY: 安全依据：上下文只由此守卫销毁一次，执行回调时动态库仍已加载。
            unsafe { (self.destroy)(self.context) };
        }
    }
    let _guard = Guard { context, destroy };
    let input: Vec<i16> = (0..160)
        .map(|i| ((i as f64 * 0.2).sin() * 20_000.0) as i16)
        .collect();
    ensure!(
        codec.max_pcm_samples >= input.len() as u32,
        "codec cannot accept the test frame"
    );
    let mut encoded = vec![0u8; codec.max_encoded_bytes as usize];
    let mut encoded_len = 0;
    ensure!(
        // SAFETY: 安全依据：输入和输出存储均有效、互不重叠，容量使用 ABI 约定的单位；
        // 可信插件必须遵守容量，返回后再检查其报告的编码长度。
        unsafe {
            encode(
                context,
                input.as_ptr(),
                input.len(),
                encoded.as_mut_ptr(),
                encoded.len(),
                &mut encoded_len,
            )
        } == 0,
        "encode failed"
    );
    ensure!(
        encoded_len > 0 && encoded_len <= encoded.len(),
        "codec returned invalid encoded length"
    );
    let mut decoded = vec![0i16; codec.max_pcm_samples as usize];
    let mut decoded_len = 0;
    ensure!(
        // SAFETY: 安全依据：编码输入长度已校验，PCM 输出按描述符分配，二者互不重叠且存活；
        // 可信插件必须遵守样本容量，返回后再检查解码长度。
        unsafe {
            decode(
                context,
                encoded.as_ptr(),
                encoded_len,
                decoded.as_mut_ptr(),
                decoded.len(),
                &mut decoded_len,
            )
        } == 0,
        "decode failed"
    );
    ensure!(
        decoded_len == input.len() && decoded_len <= decoded.len(),
        "incorrect decoded length"
    );
    let max_error = input
        .iter()
        .zip(&decoded)
        .map(|(a, b)| (i32::from(*a) - i32::from(*b)).abs())
        .max()
        .unwrap_or(0);
    ensure!(
        max_error <= 1024,
        "G.711 round-trip error too high: {max_error}"
    );
    println!("{{\"abi_version\":1,\"samples\":{decoded_len},\"max_absolute_error\":{max_error},\"passed\":true}}");
    Ok(())
}
