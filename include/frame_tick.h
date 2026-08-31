/**
 * @file frame_tick.h
 * Monotonic millisecond source for LV_TICK_CUSTOM.
 *
 * Included by LVGL's C sources, so this header must stay C-compatible.
 */

#ifndef FRAME_TICK_H
#define FRAME_TICK_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** Milliseconds since the first call. Monotonic; unaffected by wall-clock changes. */
uint32_t frame_tick_get_ms(void);

#ifdef __cplusplus
}
#endif

#endif /* FRAME_TICK_H */
