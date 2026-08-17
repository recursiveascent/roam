# Sanitizer checks for netmon_darwin.c. Run from the repo root:
#
#     make -C internal/netmon/testdata check
#
# Go's race detector does not instrument C, so these checks need their
# own build. Binaries land in the repo's gitignored build/ directory.

CC := xcrun clang
BUILD := ../../../build/netmon-testdata
CFLAGS := -g -O1 -Wall -Wextra -I..
SRCS := harness.c ../netmon_darwin.c

check: check-tsan check-asan

check-tsan: $(BUILD)/harness_tsan
	TSAN_OPTIONS="halt_on_error=1 abort_on_error=1" $(BUILD)/harness_tsan

check-asan: $(BUILD)/harness_asan
	$(BUILD)/harness_asan

$(BUILD)/harness_tsan: $(SRCS) ../netmon_darwin.h | $(BUILD)
	$(CC) $(CFLAGS) -fsanitize=thread $(SRCS) -framework Network -o $@

# The SDK's dispatch_once assumption can be false against newer host
# libdispatch; builtin sanitizer otherwise aborts in the SDK header.
$(BUILD)/harness_asan: $(SRCS) ../netmon_darwin.h | $(BUILD)
	$(CC) $(CFLAGS) -fsanitize=address,undefined,nullability \
		-fno-sanitize=builtin -fno-sanitize-recover=all $(SRCS) \
		-framework Network -o $@

$(BUILD):
	mkdir -p $(BUILD)

clean:
	rm -rf $(BUILD)

.PHONY: check check-tsan check-asan clean
