//! 对操作者显式指定的受信动态库进行有界调用；不自动搜索当前目录，不修改系统库。
use anyhow::{ensure, Context, Result};
use libloading::Library;
use std::{ffi::c_void, path::Path};

/// 官方Opus状态创建/销毁和编码调用约定；samples按每声道计算。
type OpusCreateEncoder = unsafe extern "C" fn(i32, i32, i32, *mut i32) -> *mut c_void;
type OpusCreateDecoder = unsafe extern "C" fn(i32, i32, *mut i32) -> *mut c_void;
type Destroy = unsafe extern "C" fn(*mut c_void);
type OpusEncode = unsafe extern "C" fn(*mut c_void, *const i16, i32, *mut u8, i32) -> i32;
type OpusPacketSamples = unsafe extern "C" fn(*const u8, i32, i32) -> i32;
type OpusDecode = unsafe extern "C" fn(*mut c_void, *const u8, i32, *mut i16, i32, i32) -> i32;
/// 官方SpanDSP G722调用约定；编码输入为样本数，解码输入为字节数。
type G722Init = unsafe extern "C" fn(*mut c_void, i32, i32) -> *mut c_void;
type G722Free = unsafe extern "C" fn(*mut c_void) -> i32;
type G722Encode = unsafe extern "C" fn(*mut c_void, *mut u8, *const i16, i32) -> i32;
type G722Decode = unsafe extern "C" fn(*mut c_void, *mut i16, *const u8, i32) -> i32;

/// 每个实例独占编码器和解码器；库句柄最后销毁，回调不会引用已经卸载的代码。
pub struct NativeCodec {
    /// 分开的编码和解码状态，不能互相冒用上下文。
    encoder: *mut c_void,
    decoder: *mut c_void,
    /// 从库符号表复制出的正确ABI回调。
    functions: Functions,
    /// PCM每秒样本数；Opus线上时间戳不受此配置影响。
    sample_rate: u32,
    /// 声明顺序最后，保证所有状态先销毁后才卸载原生代码。
    _library: Library,
}
/// 区分两个官方 ABI，避免把采样数/字节数参数错传到另一种调用约定。
enum Functions {
    Opus {
        encode: OpusEncode,
        decode: OpusDecode,
        packet_samples: OpusPacketSamples,
        destroy_encoder: Destroy,
        destroy_decoder: Destroy,
    },
    G722 {
        encode: G722Encode,
        decode: G722Decode,
        free_encoder: G722Free,
        free_decoder: G722Free,
    },
}
impl NativeCodec {
    /// 加载固定符号；保留旧16k入口，新增Opus48k，G.722固定16k。
    pub fn load(name: &str, path: &Path, sample_rate: u32) -> Result<Self> {
        ensure!(
            (name == "OPUS" && [16_000, 48_000].contains(&sample_rate))
                || (name == "G722" && sample_rate == 16_000),
            "unsupported native PCM sample rate"
        );
        // SAFETY: 操作者指定受信原生库；所有符号类型严格对应官方头文件，句柄覆盖状态生命周期。
        unsafe {
            let library = Library::new(path).context("load trusted codec library")?;
            let (encoder, decoder, functions) = match name {
                "OPUS" => {
                    let create_encoder: OpusCreateEncoder =
                        *library.get(b"opus_encoder_create\0")?;
                    let create_decoder: OpusCreateDecoder =
                        *library.get(b"opus_decoder_create\0")?;
                    let encode = *library.get::<OpusEncode>(b"opus_encode\0")?;
                    let decode = *library.get::<OpusDecode>(b"opus_decode\0")?;
                    let packet_samples =
                        *library.get::<OpusPacketSamples>(b"opus_packet_get_nb_samples\0")?;
                    let destroy_encoder = *library.get::<Destroy>(b"opus_encoder_destroy\0")?;
                    let destroy_decoder = *library.get::<Destroy>(b"opus_decoder_destroy\0")?;
                    let mut error = 0;
                    let enc = create_encoder(sample_rate as i32, 1, 2048, &mut error);
                    ensure!(
                        !enc.is_null() && error == 0,
                        "Opus encoder allocation failed: {error}"
                    );
                    let dec = create_decoder(sample_rate as i32, 1, &mut error);
                    if dec.is_null() || error != 0 {
                        destroy_encoder(enc);
                        anyhow::bail!("Opus decoder allocation failed: {error}");
                    }
                    (
                        enc,
                        dec,
                        Functions::Opus {
                            encode,
                            decode,
                            packet_samples,
                            destroy_encoder,
                            destroy_decoder,
                        },
                    )
                }
                "G722" => {
                    let create_encoder = *library.get::<G722Init>(b"g722_encode_init\0")?;
                    let create_decoder = *library.get::<G722Init>(b"g722_decode_init\0")?;
                    let encode = *library.get::<G722Encode>(b"g722_encode\0")?;
                    let decode = *library.get::<G722Decode>(b"g722_decode\0")?;
                    let free_encoder = *library.get::<G722Free>(b"g722_encode_free\0")?;
                    let free_decoder = *library.get::<G722Free>(b"g722_decode_free\0")?;
                    let enc = create_encoder(std::ptr::null_mut(), 64000, 0);
                    ensure!(!enc.is_null(), "G722 encoder allocation failed");
                    let dec = create_decoder(std::ptr::null_mut(), 64000, 0);
                    if dec.is_null() {
                        free_encoder(enc);
                        anyhow::bail!("G722 decoder allocation failed");
                    }
                    (
                        enc,
                        dec,
                        Functions::G722 {
                            encode,
                            decode,
                            free_encoder,
                            free_decoder,
                        },
                    )
                }
                _ => anyhow::bail!("unsupported native codec"),
            };
            Ok(Self {
                encoder,
                decoder,
                functions,
                sample_rate,
                _library: library,
            })
        }
    }
    /// 仅接收规范帧；输出容量按官方上界分配，所有返回长度再次校验。
    pub fn encode(&mut self, pcm: &[i16]) -> Result<Vec<u8>> {
        ensure!(
            [
                self.sample_rate as usize / 100,
                self.sample_rate as usize / 50
            ]
            .contains(&pcm.len()),
            "PCM must be 10/20ms"
        );
        let mut data = vec![0u8; 1275];
        // SAFETY: 独占有效状态；PCM 和输出分别覆盖声明容量，两个 ABI 的长度单位已分别匹配。
        let size = unsafe {
            match self.functions {
                Functions::Opus { encode, .. } => encode(
                    self.encoder,
                    pcm.as_ptr(),
                    pcm.len() as i32,
                    data.as_mut_ptr(),
                    data.len() as i32,
                ),
                Functions::G722 { encode, .. } => encode(
                    self.encoder,
                    data.as_mut_ptr(),
                    pcm.as_ptr(),
                    pcm.len() as i32,
                ),
            }
        };
        ensure!(
            size > 0 && (size as usize) <= data.len(),
            "native encode failed: {size}"
        );
        data.truncate(size as usize);
        Ok(data)
    }
    /// Opus 的空指针输入明确触发内建 PLC；G.722 不支持该 API，必须由上层显式处理缺包。
    pub fn decode(&mut self, data: Option<&[u8]>, samples: usize) -> Result<Vec<i16>> {
        ensure!(
            [
                self.sample_rate as usize / 100,
                self.sample_rate as usize / 50
            ]
            .contains(&samples),
            "PCM must be 10/20ms"
        );
        let bytes = data.unwrap_or(&[]);
        ensure!(bytes.len() <= 1275, "encoded packet too large");
        // 先只读检查Opus实际包时长，再调用有状态解码，错误时长不能污染下一帧预测器。
        if let (Functions::Opus { packet_samples, .. }, Some(bytes)) = (&self.functions, data) {
            ensure!(!bytes.is_empty(), "empty packet is not packet loss");
            // SAFETY: 非空码流存活且长度不超过1275；官方解析器只借用声明的字节范围。
            let count = unsafe {
                packet_samples(bytes.as_ptr(), bytes.len() as i32, self.sample_rate as i32)
            };
            ensure!(
                count == samples as i32,
                "native packet duration mismatch: {count}"
            );
        }
        let mut pcm = vec![0i16; samples];
        // SAFETY: 原生解码器独占；Opus只接受预检过的准确10/20ms容量，G722输入严格为目标样本一半。
        let size = unsafe {
            match self.functions {
                Functions::Opus { decode, .. } => decode(
                    self.decoder,
                    if data.is_some() {
                        bytes.as_ptr()
                    } else {
                        std::ptr::null()
                    },
                    bytes.len() as i32,
                    pcm.as_mut_ptr(),
                    samples as i32,
                    0,
                ),
                Functions::G722 { decode, .. } => {
                    ensure!(
                        data.is_some() && bytes.len() == samples / 2,
                        "invalid G722 frame length"
                    );
                    decode(
                        self.decoder,
                        pcm.as_mut_ptr(),
                        bytes.as_ptr(),
                        bytes.len() as i32,
                    )
                }
            }
        };
        ensure!(
            size == samples as i32,
            "native decoded duration mismatch: {size}"
        );
        pcm.truncate(samples);
        Ok(pcm)
    }
}
impl Drop for NativeCodec {
    /// 释放两份独立状态后，Rust 自动卸载最后一个库句柄。
    fn drop(&mut self) {
        // SAFETY: 成功构造才创建本对象；状态只释放一次，库仍存活且释放函数 ABI 匹配。
        unsafe {
            match self.functions {
                Functions::Opus {
                    destroy_encoder,
                    destroy_decoder,
                    ..
                } => {
                    destroy_encoder(self.encoder);
                    destroy_decoder(self.decoder);
                }
                Functions::G722 {
                    free_encoder,
                    free_decoder,
                    ..
                } => {
                    free_encoder(self.encoder);
                    free_decoder(self.decoder);
                }
            }
        }
    }
}
