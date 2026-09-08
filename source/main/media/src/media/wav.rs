//! 有界 WAV 提示音解析：只接受 8 kHz 单声道 PCM16、A-law 与 μ-law。
//! 仅解析容器并恢复 PCM 样本，不重采样，不执行未知 chunk，也不承诺听感质量。

use anyhow::{bail, ensure, Result};

/// 输入文件的硬上限；调用方读取文件时也应使用此限制。
pub(super) const MAX_WAV_BYTES: usize = 1_048_576;
/// 8 kHz 下最多 30 秒，限制解码后的内存及后续提示音长度。
pub(super) const MAX_SAMPLES: usize = 240_000;
/// 限制空块和元数据块的数量，避免大量小块消耗解析时间。
const MAX_CHUNKS: usize = 128;

/// 本模块支持的三种样本编码；所有格式均为 8 kHz 单声道。
#[derive(Clone, Copy, Debug)]
enum Encoding {
    Pcm16,
    ALaw,
    MuLaw,
}

impl Encoding {
    /// 每个单声道样本占用的字节数，同时也是合法的 block_align。
    fn bytes_per_sample(self) -> usize {
        match self {
            Self::Pcm16 => 2,
            Self::ALaw | Self::MuLaw => 1,
        }
    }
}

/// 校验 fmt 块中的编码与派生字段，拒绝扩展格式和隐含格式转换。
fn parse_format(format: &[u8]) -> Result<Encoding> {
    ensure!(format.len() >= 16, "WAV fmt chunk is shorter than 16 bytes");
    let tag = u16::from_le_bytes([format[0], format[1]]);
    let encoding = match tag {
        1 => {
            ensure!(
                matches!(format.len(), 16 | 18),
                "PCM WAV fmt chunk must contain 16 or 18 bytes"
            );
            Encoding::Pcm16
        }
        6 | 7 => {
            ensure!(
                format.len() == 18,
                "G.711 WAV fmt chunk must contain 18 bytes"
            );
            if tag == 6 {
                Encoding::ALaw
            } else {
                Encoding::MuLaw
            }
        }
        // IEEE float、extensible 及其他编码均不能作为此受限播放器的输入。
        _ => bail!("unsupported WAV format tag: {tag}"),
    };
    if format.len() == 18 {
        ensure!(
            format[16..18] == [0, 0],
            "WAV fmt extension size must be zero"
        );
    }

    let channels = u16::from_le_bytes([format[2], format[3]]);
    let sample_rate = u32::from_le_bytes([format[4], format[5], format[6], format[7]]);
    let byte_rate = u32::from_le_bytes([format[8], format[9], format[10], format[11]]);
    let block_align = u16::from_le_bytes([format[12], format[13]]);
    let bits_per_sample = u16::from_le_bytes([format[14], format[15]]);
    let sample_bytes = encoding.bytes_per_sample();
    ensure!(channels == 1, "WAV must contain exactly one channel");
    ensure!(sample_rate == 8_000, "WAV sample rate must be 8000 Hz");
    ensure!(
        usize::from(block_align) == sample_bytes,
        "WAV block alignment does not match its encoding"
    );
    ensure!(
        usize::from(bits_per_sample) == sample_bytes * 8,
        "WAV bit depth does not match its encoding"
    );
    ensure!(
        u64::from(byte_rate) == 8_000 * sample_bytes as u64,
        "WAV byte rate does not match its encoding"
    );
    Ok(encoding)
}

/// 将严格校验的 RIFF little-endian WAVE 解码为有界的有符号 PCM 样本。
pub(super) fn decode_wav(bytes: &[u8]) -> Result<Vec<i16>> {
    ensure!(bytes.len() <= MAX_WAV_BYTES, "WAV file exceeds 1 MiB");
    ensure!(bytes.len() >= 12, "WAV RIFF header is incomplete");
    ensure!(&bytes[..4] == b"RIFF", "WAV must use little-endian RIFF");
    ensure!(&bytes[8..12] == b"WAVE", "RIFF form type must be WAVE");
    let riff_size = u32::from_le_bytes([bytes[4], bytes[5], bytes[6], bytes[7]]);
    ensure!(
        u64::from(riff_size) + 8 == bytes.len() as u64,
        "WAV RIFF length must exactly match the file length"
    );

    let mut cursor = 12usize;
    let mut chunk_count = 0usize;
    let mut encoding = None;
    let mut data = None;
    let mut fact_samples = None;
    while cursor < bytes.len() {
        chunk_count += 1;
        ensure!(chunk_count <= MAX_CHUNKS, "WAV contains too many chunks");
        ensure!(bytes.len() - cursor >= 8, "WAV chunk header is incomplete");
        let chunk_id = &bytes[cursor..cursor + 4];
        let size = u32::from_le_bytes([
            bytes[cursor + 4],
            bytes[cursor + 5],
            bytes[cursor + 6],
            bytes[cursor + 7],
        ]) as usize;
        let start = cursor + 8;
        // 先检查剩余长度再相加，杜绝恶意 chunk 长度导致溢出或越界切片。
        ensure!(
            size <= bytes.len() - start,
            "WAV chunk payload is incomplete"
        );
        let end = start + size;
        let padding = size & 1;
        ensure!(
            padding <= bytes.len() - end,
            "WAV odd-sized chunk is missing its padding byte"
        );
        if padding != 0 {
            ensure!(bytes[end] == 0, "WAV chunk padding byte must be zero");
        }
        let payload = &bytes[start..end];
        match chunk_id {
            b"fmt " => {
                ensure!(encoding.is_none(), "WAV contains duplicate fmt chunks");
                encoding = Some(parse_format(payload)?);
            }
            b"data" => {
                ensure!(data.is_none(), "WAV contains duplicate data chunks");
                ensure!(encoding.is_some(), "WAV fmt chunk must precede data");
                data = Some(payload);
            }
            b"fact" => {
                ensure!(fact_samples.is_none(), "WAV contains duplicate fact chunks");
                // 只接受可完全验证的标准单字段 fact，不忽略未知扩展或错误样本数。
                ensure!(payload.len() == 4, "WAV fact chunk must contain 4 bytes");
                fact_samples = Some(u32::from_le_bytes([
                    payload[0], payload[1], payload[2], payload[3],
                ]));
            }
            // JUNK、LIST 等未知块只做长度及对齐检查；其内容从不作为指令执行。
            _ => {}
        }
        cursor = end + padding;
    }

    let encoding = encoding.ok_or_else(|| anyhow::anyhow!("WAV is missing its fmt chunk"))?;
    let data = data.ok_or_else(|| anyhow::anyhow!("WAV is missing its data chunk"))?;
    let sample_bytes = encoding.bytes_per_sample();
    ensure!(!data.is_empty(), "WAV data must not be empty");
    ensure!(
        data.len() % sample_bytes == 0,
        "WAV data does not contain whole samples"
    );
    let sample_count = data.len() / sample_bytes;
    ensure!(sample_count <= MAX_SAMPLES, "WAV audio exceeds 30 seconds");
    if let Some(fact_samples) = fact_samples {
        ensure!(
            u64::from(fact_samples) == sample_count as u64,
            "WAV fact sample count does not match data"
        );
    }

    // 所有容器和样本上限均已验证后才分配输出，不接受部分成功或截断播放。
    Ok(match encoding {
        Encoding::Pcm16 => data
            .chunks_exact(2)
            .map(|sample| i16::from_le_bytes([sample[0], sample[1]]))
            .collect(),
        Encoding::ALaw | Encoding::MuLaw => data
            .iter()
            .map(|&value| crate::audio::g711::decode(value, matches!(encoding, Encoding::ALaw)))
            .collect(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 生成固定 8 kHz 单声道的标准 fmt，供纯内存容器测试使用。
    fn format(tag: u16) -> Vec<u8> {
        let sample_bytes: u16 = if tag == 1 { 2 } else { 1 };
        let mut format = Vec::new();
        format.extend_from_slice(&tag.to_le_bytes());
        format.extend_from_slice(&1u16.to_le_bytes());
        format.extend_from_slice(&8_000u32.to_le_bytes());
        format.extend_from_slice(&(8_000 * u32::from(sample_bytes)).to_le_bytes());
        format.extend_from_slice(&sample_bytes.to_le_bytes());
        format.extend_from_slice(&(sample_bytes * 8).to_le_bytes());
        if tag != 1 {
            format.extend_from_slice(&0u16.to_le_bytes());
        }
        format
    }

    /// 将测试块封装为 RIFF；奇数字节块自动加零 padding。
    fn container(chunks: &[([u8; 4], Vec<u8>)]) -> Vec<u8> {
        let mut bytes = b"RIFF\0\0\0\0WAVE".to_vec();
        for (id, payload) in chunks {
            bytes.extend_from_slice(id);
            bytes.extend_from_slice(&(payload.len() as u32).to_le_bytes());
            bytes.extend_from_slice(payload);
            if payload.len() & 1 != 0 {
                bytes.push(0);
            }
        }
        update_riff_size(&mut bytes);
        bytes
    }

    /// 单独更新外层长度，便于构造内部 chunk 损坏但 RIFF 长度正确的输入。
    fn update_riff_size(bytes: &mut [u8]) {
        let size = (bytes.len() - 8) as u32;
        bytes[4..8].copy_from_slice(&size.to_le_bytes());
    }

    /// 构造最小合法格式与数据组合。
    fn wav(tag: u16, data: &[u8]) -> Vec<u8> {
        container(&[(*b"fmt ", format(tag)), (*b"data", data.to_vec())])
    }

    #[test]
    /// PCM 按 little-endian 还原正负边界值；fmt16 和 cbSize=0 的 fmt18 均合法。
    fn pcm16_known_samples_and_both_fmt_sizes() {
        let data = [0x00, 0x80, 0xff, 0xff, 0, 0, 1, 0, 0xff, 0x7f];
        let expected = vec![i16::MIN, -1, 0, 1, i16::MAX];
        assert_eq!(decode_wav(&wav(1, &data)).unwrap(), expected);
        let mut format = format(1);
        format.extend_from_slice(&[0, 0]);
        let bytes = container(&[(*b"fmt ", format), (*b"data", data.to_vec())]);
        assert_eq!(decode_wav(&bytes).unwrap(), expected);
    }

    #[test]
    /// A-law 码字使用独立的标准已知值验证，并验证可选 fact 的样本数。
    fn alaw_known_samples_with_fact() {
        let bytes = container(&[
            (*b"fmt ", format(6)),
            (*b"fact", 6u32.to_le_bytes().to_vec()),
            (*b"data", vec![0xd5, 0x55, 0xaa, 0x2a, 0x80, 0x00]),
        ]);
        assert_eq!(
            decode_wav(&bytes).unwrap(),
            vec![8, -8, 32256, -32256, 5504, -5504]
        );
        assert_eq!(decode_wav(&wav(6, &[0xd5])).unwrap(), vec![8]);
    }

    #[test]
    /// μ-law 码字使用独立的已知值；fact 在 data 后也必须参与完整校验。
    fn mulaw_known_samples_with_trailing_fact() {
        let data = vec![0xff, 0x7f, 0x80, 0x00, 0xd5, 0x55];
        let bytes = container(&[
            (*b"fmt ", format(7)),
            (*b"data", data.clone()),
            (*b"fact", 6u32.to_le_bytes().to_vec()),
        ]);
        let expected = vec![0, 0, 32124, -32124, 716, -716];
        assert_eq!(decode_wav(&bytes).unwrap(), expected);
        assert_eq!(decode_wav(&wav(7, &data)).unwrap(), expected);
    }

    #[test]
    /// 错误容器签名、大端与 RF64 容器、非 WAVE 类型及不完整头全部拒绝。
    fn rejects_wrong_containers_and_short_headers() {
        let good = wav(1, &[0, 0]);
        for length in 0..12 {
            assert!(decode_wav(&good[..length]).is_err(), "length {length}");
        }
        for signature in [b"RIFX", b"RF64", b"NOPE"] {
            let mut bytes = good.clone();
            bytes[..4].copy_from_slice(signature);
            assert!(decode_wav(&bytes).is_err());
        }
        let mut bytes = good;
        bytes[8..12].copy_from_slice(b"AIFF");
        assert!(decode_wav(&bytes).is_err());
    }

    #[test]
    /// RIFF 声明长度必须恰等于整个输入，不能接受尾随数据或截断文件。
    fn rejects_inexact_riff_lengths() {
        let good = wav(1, &[0, 0]);
        for size in [0, good.len() as u32 - 9, good.len() as u32 - 7, u32::MAX] {
            let mut bytes = good.clone();
            bytes[4..8].copy_from_slice(&size.to_le_bytes());
            assert!(decode_wav(&bytes).is_err(), "declared size {size}");
        }
        let mut trailing = good.clone();
        trailing.push(0);
        assert!(decode_wav(&trailing).is_err());
        assert!(decode_wav(&good[..good.len() - 1]).is_err());
    }

    #[test]
    /// 外层 RIFF 长度正确时，内部块的短头、超长 payload 和越界长度仍须拒绝。
    fn rejects_incomplete_chunk_headers_and_payloads() {
        for tail_size in 1..8 {
            let mut bytes = wav(1, &[0, 0]);
            bytes.extend(vec![0; tail_size]);
            update_riff_size(&mut bytes);
            assert!(decode_wav(&bytes).is_err(), "tail size {tail_size}");
        }
        for claimed_size in [3u32, u32::MAX] {
            let mut bytes = wav(1, &[0, 0]);
            bytes[40..44].copy_from_slice(&claimed_size.to_le_bytes());
            assert!(decode_wav(&bytes).is_err());
        }
    }

    #[test]
    /// fmt 与 data 必须唯一且 fmt 在前；缺块与重复块均不能产生部分音频。
    fn rejects_missing_duplicate_and_out_of_order_chunks() {
        let fmt = (*b"fmt ", format(1));
        let data = (*b"data", vec![0, 0]);
        for chunks in [
            vec![],
            vec![fmt.clone()],
            vec![data.clone()],
            vec![data.clone(), fmt.clone()],
            vec![fmt.clone(), fmt.clone(), data.clone()],
            vec![fmt.clone(), data.clone(), fmt.clone()],
            vec![fmt.clone(), data.clone(), data.clone()],
        ] {
            assert!(decode_wav(&container(&chunks)).is_err());
        }
    }

    #[test]
    /// 不支持的编码不得借用 PCM/G.711 的其他字段绕过格式筛选。
    fn rejects_unsupported_format_tags() {
        for tag in [0u16, 2, 3, 8, 0xfffe, u16::MAX] {
            let mut fmt = format(1);
            fmt[..2].copy_from_slice(&tag.to_le_bytes());
            let bytes = container(&[(*b"fmt ", fmt), (*b"data", vec![0, 0])]);
            assert!(decode_wav(&bytes).is_err(), "format tag {tag}");
        }
    }

    #[test]
    /// 拒绝截断或多余 fmt 字节，并且 G.711 必须带零 cbSize。
    fn rejects_invalid_fmt_lengths_and_extensions() {
        for tag in [1, 6, 7] {
            for length in [0, 1, 15, 16, 17, 18, 19, 20, 40] {
                if length == 18 || (tag == 1 && length == 16) {
                    continue;
                }
                let mut fmt = format(tag);
                fmt.resize(length, 0);
                let bytes = container(&[(*b"fmt ", fmt), (*b"data", vec![0, 0])]);
                assert!(decode_wav(&bytes).is_err(), "tag {tag}, length {length}");
            }
            for extension_size in [1u16, 2, 256, u16::MAX] {
                let mut fmt = format(tag);
                fmt.resize(18, 0);
                fmt[16..18].copy_from_slice(&extension_size.to_le_bytes());
                let bytes = container(&[(*b"fmt ", fmt), (*b"data", vec![0, 0])]);
                assert!(decode_wav(&bytes).is_err());
            }
        }
    }

    #[test]
    /// 声道、采样率和所有派生字段都逐项校验，禁止隐式重采样或格式修复。
    fn rejects_inconsistent_format_fields() {
        for tag in [1, 6, 7] {
            for (offset, values) in [
                (2, vec![0u16, 2, u16::MAX]),
                (12, vec![0, if tag == 1 { 1 } else { 2 }, u16::MAX]),
                (14, vec![0, if tag == 1 { 8 } else { 16 }, u16::MAX]),
            ] {
                for value in values {
                    let mut fmt = format(tag);
                    fmt[offset..offset + 2].copy_from_slice(&value.to_le_bytes());
                    let bytes = container(&[(*b"fmt ", fmt), (*b"data", vec![0, 0])]);
                    assert!(decode_wav(&bytes).is_err(), "tag {tag}, offset {offset}");
                }
            }
            for (offset, values) in [
                (4, vec![0u32, 7_999, 16_000, u32::MAX]),
                (
                    8,
                    vec![0, 8_001, if tag == 1 { 8_000 } else { 16_000 }, u32::MAX],
                ),
            ] {
                for value in values {
                    let mut fmt = format(tag);
                    fmt[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
                    let bytes = container(&[(*b"fmt ", fmt), (*b"data", vec![0, 0])]);
                    assert!(decode_wav(&bytes).is_err(), "tag {tag}, offset {offset}");
                }
            }
        }
    }

    #[test]
    /// fact 必须可完全验证，拒绝短块、扩展块、错误样本数与重复块。
    fn rejects_invalid_fact_chunks() {
        for tag in [1, 6, 7] {
            let data = if tag == 1 { vec![0, 0] } else { vec![0xff] };
            for fact in [
                vec![],
                vec![1, 0, 0],
                vec![1, 0, 0, 0, 0],
                0u32.to_le_bytes().to_vec(),
                2u32.to_le_bytes().to_vec(),
                u32::MAX.to_le_bytes().to_vec(),
            ] {
                let bytes = container(&[
                    (*b"fmt ", format(tag)),
                    (*b"fact", fact),
                    (*b"data", data.clone()),
                ]);
                assert!(decode_wav(&bytes).is_err(), "format tag {tag}");
            }
            let fact = (*b"fact", 1u32.to_le_bytes().to_vec());
            let bytes = container(&[
                (*b"fmt ", format(tag)),
                fact.clone(),
                (*b"data", data),
                fact,
            ]);
            assert!(decode_wav(&bytes).is_err());
        }
    }

    #[test]
    /// 未知块允许在各位置出现，内容不影响音频；奇数块必须有零 padding。
    fn unknown_chunks_and_odd_padding_are_checked() {
        let bytes = container(&[
            (*b"JUNK", vec![1, 2, 3]),
            (*b"fmt ", format(7)),
            (*b"LIST", b"fmt data RIFF".to_vec()),
            (*b"data", vec![0xff]),
            (*b"tail", vec![0x80]),
        ]);
        assert_eq!(decode_wav(&bytes).unwrap(), vec![0]);
        for tag in [1, 7] {
            let mut bytes = container(&[
                (*b"fmt ", format(tag)),
                (*b"data", vec![0, 0]),
                (*b"JUNK", vec![1]),
            ]);
            *bytes.last_mut().unwrap() = 1;
            assert!(decode_wav(&bytes).is_err());
            bytes.pop();
            update_riff_size(&mut bytes);
            assert!(decode_wav(&bytes).is_err());
        }
        let mut data_padding = wav(7, &[0xff]);
        *data_padding.last_mut().unwrap() = 0xff;
        assert!(decode_wav(&data_padding).is_err());
        data_padding.pop();
        update_riff_size(&mut data_padding);
        assert!(decode_wav(&data_padding).is_err());
    }

    #[test]
    /// 空音频和 PCM 半个样本均无有效播放含义，必须拒绝。
    fn rejects_empty_data_and_partial_pcm_samples() {
        for tag in [1, 6, 7] {
            assert!(decode_wav(&wav(tag, &[])).is_err());
        }
        assert!(decode_wav(&wav(1, &[0])).is_err());
        assert!(decode_wav(&wav(1, &[0, 0, 0])).is_err());
    }

    #[test]
    /// 三种编码都接受恰好 30 秒并拒绝多一个样本，不能静默截断。
    fn sample_limit_is_inclusive_for_all_encodings() {
        for tag in [1, 6, 7] {
            let sample_bytes = if tag == 1 { 2 } else { 1 };
            let exact = vec![0; MAX_SAMPLES * sample_bytes];
            assert_eq!(decode_wav(&wav(tag, &exact)).unwrap().len(), MAX_SAMPLES);
            let too_long = vec![0; (MAX_SAMPLES + 1) * sample_bytes];
            assert!(decode_wav(&wav(tag, &too_long)).is_err());
        }
    }

    #[test]
    /// 文件大小包含元数据与 padding；1 MiB 合法边界可接受，再增加即拒绝。
    fn file_size_limit_includes_metadata() {
        let fixed_size = wav(1, &[0, 0]).len() + 8;
        let make = |metadata_size| {
            container(&[
                (*b"fmt ", format(1)),
                (*b"JUNK", vec![0; metadata_size]),
                (*b"data", vec![0, 0]),
            ])
        };
        let exact = make(MAX_WAV_BYTES - fixed_size);
        assert_eq!(exact.len(), MAX_WAV_BYTES);
        assert_eq!(decode_wav(&exact).unwrap(), vec![0]);
        let too_large = make(MAX_WAV_BYTES - fixed_size + 2);
        assert_eq!(too_large.len(), MAX_WAV_BYTES + 2);
        assert!(decode_wav(&too_large).is_err());
    }

    #[test]
    /// 零长度未知块也计数；最多 128 块，防止小块数量绕过文件大小限制。
    fn chunk_count_limit_includes_empty_unknown_chunks() {
        let mut chunks = vec![(*b"fmt ", format(1)), (*b"data", vec![0, 0])];
        chunks.extend((2..MAX_CHUNKS).map(|_| (*b"JUNK", vec![])));
        assert_eq!(decode_wav(&container(&chunks)).unwrap(), vec![0]);
        chunks.push((*b"JUNK", vec![]));
        assert!(decode_wav(&container(&chunks)).is_err());
    }
}
