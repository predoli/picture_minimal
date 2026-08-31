# Cross-compiling the frontend for a Raspberry Pi running 64-bit Raspberry Pi OS.
#
# Used by CI inside a pinned debian:bookworm container, with Debian's own
# g++-aarch64-linux-gnu and libdrm-dev:arm64. Building in Bookworm is the point:
# it is what the frames run, so the binary's glibc requirement cannot come out
# newer than the 2.36 they have. A build on the CI runner's own Ubuntu links
# against 2.39 and simply refuses to start on the Pi.
#
#   cmake -B build -G Ninja -DFRAME_DISPLAY=drm \
#         -DCMAKE_TOOLCHAIN_FILE="$PWD/deploy/aarch64-linux-gnu.cmake"

set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR aarch64)

set(CMAKE_C_COMPILER aarch64-linux-gnu-gcc)
set(CMAKE_CXX_COMPILER aarch64-linux-gnu-g++)

# Debian keeps foreign-architecture libraries in the multiarch directory rather
# than in the cross toolchain's sysroot, so both have to be reachable.
set(CMAKE_FIND_ROOT_PATH /usr/aarch64-linux-gnu)
set(CMAKE_LIBRARY_ARCHITECTURE aarch64-linux-gnu)

# Build tools must come from the host; libraries and headers must not.
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)

# pkg-config would otherwise answer with the host's x86_64 libdrm, and the
# mismatch surfaces as a wall of linker errors rather than as a clear message.
set(ENV{PKG_CONFIG_LIBDIR} "/usr/lib/aarch64-linux-gnu/pkgconfig:/usr/share/pkgconfig")
set(ENV{PKG_CONFIG_SYSROOT_DIR} "")
