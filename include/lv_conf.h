/**
 * @file lv_conf.h
 * Configuration for LVGL 8.3.x as used by the picture frame frontend.
 *
 * Only overrides live here. Anything not defined falls back to the default in
 * lv_conf_internal.h, so this file stays readable rather than mirroring the
 * whole upstream template.
 *
 * Reached via -DLV_CONF_INCLUDE_SIMPLE with include/ ahead of libs/lvgl/ on the
 * include path.
 */

#ifndef LV_CONF_H
#define LV_CONF_H

#include <stdint.h>

/*====================
   COLOR
 *====================*/

/* RGB565. The Go backend packs blobs in exactly this layout, so changing the
 * depth or the byte order here silently corrupts every pre-rendered image. */
#define LV_COLOR_DEPTH 16
#define LV_COLOR_16_SWAP 0

/*====================
   MEMORY
 *====================*/

/* Use libc malloc. The built-in allocator defaults to a 48 KB pool, which
 * cannot hold a single 1080p draw buffer, let alone two. */
#define LV_MEM_CUSTOM 1
#define LV_MEM_CUSTOM_INCLUDE <stdlib.h>
#define LV_MEM_CUSTOM_ALLOC malloc
#define LV_MEM_CUSTOM_FREE free
#define LV_MEM_CUSTOM_REALLOC realloc

/*====================
   TICK
 *====================*/

/* Drive the tick from a monotonic clock instead of lv_tick_inc(). The render
 * loop sleeps for up to 100 ms at a time, so incrementing the tick from there
 * would quantise every fade to the sleep interval and make it visibly jerky. */
#define LV_TICK_CUSTOM 1
#define LV_TICK_CUSTOM_INCLUDE "frame_tick.h"
#define LV_TICK_CUSTOM_SYS_TIME_EXPR (frame_tick_get_ms())

/*====================
   FEATURES
 *====================*/

/* The backend hands over pre-decoded RGB565 pixels, so LVGL never opens a file
 * or decodes an image itself. Both stay off deliberately. */
#define LV_USE_FS_STDIO 0
#define LV_USE_FS_POSIX 0
#define LV_USE_PNG 0
#define LV_USE_BMP 0
#define LV_USE_SJPG 0
#define LV_USE_GIF 0

/* Needed for the pairing screen. qrcodegen ships inside LVGL, so this costs no
 * new dependency. */
#define LV_USE_QRCODE 1

/* Larger faces for the pairing and empty-state screens, which are read from
 * across a room rather than up close. */
#define LV_FONT_MONTSERRAT_14 1
#define LV_FONT_MONTSERRAT_20 1
#define LV_FONT_MONTSERRAT_28 1

/*====================
   LOGGING
 *====================*/

#define LV_USE_LOG 1
#define LV_LOG_LEVEL LV_LOG_LEVEL_WARN
#define LV_LOG_PRINTF 1

/*====================
   DRAWING
 *====================*/

/* Full-screen cross-fades on a Cortex-A53 benefit from a slightly larger
 * gradient cache; everything else is left at defaults. */
#define LV_IMG_CACHE_DEF_SIZE 2

#endif /*LV_CONF_H*/
