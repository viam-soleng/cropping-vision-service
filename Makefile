UNAME=$(shell uname)
ifeq ($(UNAME),Linux)
	NDK_ROOT ?= $(HOME)/Android/Sdk/ndk/26.2.11394342
	HOST_OS ?= linux
	GOARCH := amd64
	CC_ARCH ?= x86_64
else
	NDK_ROOT ?= $(HOME)/Library/Android/sdk/ndk/26.2.11394342
	HOST_OS ?= darwin
	GOARCH := arm64
	CC_ARCH ?= aarch64
endif

APP_ROOT ?= $(HOME)/AndroidStudioProjects/DroidCamera

GOOS := android
CGO_ENABLED := 1
CC := $(shell realpath $(NDK_ROOT)/toolchains/llvm/prebuilt/$(HOST_OS)-x86_64/bin/$(CC_ARCH)-linux-android30-clang)
CGO_CFLAGS := -I$(NDK_ROOT)/toolchains/llvm/prebuilt/$(HOST_OS)-x86_64/sysroot/usr/include \
              -I$(NDK_ROOT)/toolchains/llvm/prebuilt/$(HOST_OS)-x86_64/sysroot/usr/include/$(CC_ARCH)-linux-android
CGO_LDFLAGS := -L$(NDK_ROOT)/toolchains/llvm/prebuilt/$(HOST_OS)-x86_64/sysroot/usr/lib
OUTPUT_DIR := bin
OUTPUT_NAME := droidcamera-android-$(CC_ARCH)
OUTPUT := $(OUTPUT_DIR)/$(OUTPUT_NAME)

ASSET_PATH := $(APP_ROOT)/app/src/main/assets/$(OUTPUT_NAME)
BINARY_PATH := /data/local/tmp/$(OUTPUT_NAME)

TARGET := android
ANDROID_API := 29
MOBILE_OUTPUT_NAME := droidcam.aar
MOBILE_OUTPUT := $(OUTPUT_DIR)/$(MOBILE_OUTPUT_NAME)

MODULE_VERSION := 0.0.3

# Build the arm64 module binary
build-binary:
	@echo "Building binary for Android..."
	@GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) \
		CGO_CFLAGS="$(CGO_CFLAGS)" \
		CGO_LDFLAGS="$(CGO_LDFLAGS)" \
		CC=$(CC) \
		go build -v -tags local_cgo,no_cgo \
		-o $(OUTPUT) .
	@echo "Build complete: $(OUTPUT)"

# Build the mobile library
build-mobile:
	@echo "Building mobile library..."
	@gomobile bind -v -target $(TARGET) -androidapi $(ANDROID_API) -o $(MOBILE_OUTPUT) ./camera/
	@echo "Mobile library built: $(MOBILE_OUTPUT)"

# Push the binary to device
push-binary:
	@echo "Pushing binary to device..."
	@adb push $(OUTPUT) $(BINARY_PATH)
	@echo "Binary pushed: $(BINARY_PATH)"

# Push the module to viam registry
push-module:
	@echo "Pushing module to viam registry..."
	@viam module upload --platform=android/arm64 --version=$(MODULE_VERSION) $(OUTPUT)
	@echo "Module pushed: $(OUTPUT)"

# Copy the binary to project assets
push-asset:
	@echo "Copying binary to project assets..."
	@cp $(OUTPUT) $(ASSET_PATH)
	@echo "Binary copied to assets: $(ASSET_PATH)"

# Enable root access and set SELinux to permissive
root:
	@echo "Enabling root access and setting SELinux to permissive..."
	@adb root && adb shell "setenforce 0"
	@echo "Root access enabled and SELinux set to permissive."

# Filter logcat for camera logs
logs:
	@echo "Filtering logcat for camera logs..."
	@adb logcat -s camera