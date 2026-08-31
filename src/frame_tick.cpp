#include "frame_tick.h"

#include <chrono>

namespace {

std::chrono::steady_clock::time_point origin()
{
    // Function-local static: initialised once, on first use, thread-safely.
    static const auto start = std::chrono::steady_clock::now();
    return start;
}

} // namespace

extern "C" uint32_t frame_tick_get_ms(void)
{
    const auto elapsed = std::chrono::steady_clock::now() - origin();
    return static_cast<uint32_t>(
        std::chrono::duration_cast<std::chrono::milliseconds>(elapsed).count());
}
