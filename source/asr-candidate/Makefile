# 构建变量允许部署机选择工具链；媒体产物可放到工作区外，避免混入源码交付包。
SHELL := /bin/sh
CARGO ?= cargo
GO ?= go
CC ?= cc
CARGO_TARGET_DIR ?= $(CURDIR)/target/media
# 真实RX验收每次使用新目录，保留SIP原帧与暂停故障记录；不会覆盖上次失败证据。
RX_TEST_ARTIFACT_ROOT ?= $(CURDIR)/run/rx-tests
UNAME := $(shell uname -s)
# 同一 C ABI 示例在 macOS 生成 dylib，在 Linux 生成 so。
ifeq ($(UNAME),Darwin)
SHARED_FLAGS := -dynamiclib
SHARED_EXT := dylib
else
SHARED_FLAGS := -shared -fPIC
SHARED_EXT := so
endif

.PHONY: all media control bench asr-mock native audio-backends audio-check docs test check run
# 四个可独立验证的产物：媒体、控制面、压测端点、编解码动态库。
all: media control bench native

media:
	$(CARGO) build --locked --release --manifest-path media/Cargo.toml --target-dir $(CARGO_TARGET_DIR)
	mkdir -p bin
	# 先写入新文件再原子替换，避免覆盖运行中映射的可执行文件及 macOS 代码签名缓存。
	cp $(CARGO_TARGET_DIR)/release/rustswitch-media bin/rustswitch-media.next
	mv -f bin/rustswitch-media.next bin/rustswitch-media

# 可选原生P0后端只写到显式私有目录，不修改系统库或默认部署配置。
AUDIO_PREFIX ?= $(CURDIR)/run/audio-libs
audio-backends:
	python3 native/audio/build_codecs.py --prefix "$(AUDIO_PREFIX)"

audio-check: media
	RUSTSWITCH_G722_LIBRARY="$(AUDIO_PREFIX)/librustswitch_g722.$(SHARED_EXT)" RUSTSWITCH_OPUS_LIBRARY="$(AUDIO_PREFIX)/opus/lib/libopus.$(SHARED_EXT)" bin/rustswitch-media --check-audio

# 构建前同步后台文档，新增 HTTP 路由缺少说明时生成器会明确失败。
docs:
	python3 tools/build_api_docs.py

control: docs
	mkdir -p bin
	cd control && $(GO) build -trimpath -o ../bin/rustswitch ./cmd/rustswitch

bench:
	mkdir -p bin
	cd control && $(GO) build -trimpath -o ../bin/callbench ./cmd/callbench

# 模拟识别进程只为验收提供独立字节接收与故障注入，不随主服务自动启动。
asr-mock:
	mkdir -p bin
	cd control && $(GO) build -trimpath -o ../bin/asr-mock ./cmd/asr-mock

native:
	mkdir -p bin
	$(CC) -std=c11 -O2 -Wall -Wextra -Werror $(SHARED_FLAGS) -I native/include native/examples/g711_plugin.c -o bin/librustswitch_g711.$(SHARED_EXT)

# 两种端到端模式共享测试端口，必须顺序运行；功能回归不能替代容量验收。
test: all asr-mock
	$(CARGO) test --locked --manifest-path media/Cargo.toml --target-dir $(CARGO_TARGET_DIR)
	mkdir -p "$(abspath $(RX_TEST_ARTIFACT_ROOT))"
	# 显式绑定本次构建的媒体程序；全Go检查同时运行真实收音，避免无环境时遗漏或误用旧程序。
	@rx_test_dir=$$(mktemp -d "$(abspath $(RX_TEST_ARTIFACT_ROOT))/run.XXXXXX") && \
		mkdir "$$rx_test_dir/sip" "$$rx_test_dir/pause" && \
		cd control && \
		RUSTSWITCH_REAL_MEDIA="$(CURDIR)/bin/rustswitch-media" \
		RUSTSWITCH_RX_SIP_ARTIFACTS="$$rx_test_dir/sip" \
		RUSTSWITCH_RX_PAUSE_ARTIFACTS="$$rx_test_dir/pause" \
		RUSTSWITCH_ASR_MOCK_BIN="$(CURDIR)/bin/asr-mock" \
		RUSTSWITCH_ASR_REAL_OUTPUT="$$rx_test_dir/asr-client" \
		RUSTSWITCH_ASR_SIP_OUTPUT="$$rx_test_dir/asr-sip" \
		$(GO) test -race -count=1 -p=1 -timeout=10m ./...
	bin/rustswitch-media --check-codec bin/librustswitch_g711.$(SHARED_EXT)
	python3 tests/e2e.py
	python3 tests/e2e.py --connected
	python3 tests/codec_e2e.py
	python3 tests/codec_e2e.py --connected

# 静态检查与竞态/通话回归分开，便于在 CI 中定位失败阶段。
check:
	python3 tools/build_api_docs.py --check
	$(CARGO) fmt --check --manifest-path media/Cargo.toml
	$(CARGO) clippy --locked --all-targets --manifest-path media/Cargo.toml --target-dir $(CARGO_TARGET_DIR) -- -D warnings
	cd control && $(GO) vet ./...

run:
	bin/rustswitch -config config/local.json
