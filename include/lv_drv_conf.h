/**
 * @file lv_drv_conf.h
 * Configuration for lv_drivers (libs/lv_drivers).
 *
 * Exactly one display backend is compiled in, selected by the CMake option
 * FRAME_DISPLAY (drm | fbdev | sdl), which defines FRAME_DISPLAY_* below.
 */

#ifndef LV_DRV_CONF_H
#define LV_DRV_CONF_H

#include "lv_conf.h"

/*====================
   DISPLAY BACKENDS
 *====================*/

#if defined(FRAME_DISPLAY_DRM)

#define USE_DRM 1
#ifndef DRM_CARD
#define DRM_CARD "/dev/dri/card0"
#endif
#define DRM_CONNECTOR_ID -1 /* -1 = first connected connector */

#elif defined(FRAME_DISPLAY_FBDEV)

#define USE_FBDEV 1
#define FBDEV_PATH "/dev/fb0"

#elif defined(FRAME_DISPLAY_SDL)

#define USE_SDL 1

/* The SDL driver fixes its window size at compile time, unlike DRM which
 * reports the real panel geometry at runtime. These are development defaults;
 * override with -DFRAME_SDL_WIDTH / -DFRAME_SDL_HEIGHT to rehearse against the
 * resolution of an actual frame. */
#ifndef FRAME_SDL_WIDTH
#define FRAME_SDL_WIDTH 1024
#endif
#ifndef FRAME_SDL_HEIGHT
#define FRAME_SDL_HEIGHT 600
#endif

#define SDL_HOR_RES FRAME_SDL_WIDTH
#define SDL_VER_RES FRAME_SDL_HEIGHT
#define SDL_ZOOM 1
#define SDL_DOUBLE_BUFFERED 0
#define SDL_INCLUDE_PATH <SDL2/SDL.h>

#else
#error "No display backend selected: define FRAME_DISPLAY_DRM, FRAME_DISPLAY_FBDEV or FRAME_DISPLAY_SDL"
#endif

/*====================
   UNUSED DRIVERS
 *====================*/

/* The frame has no input device: no touch, no mouse, no keypad. Everything the
 * user controls happens in Nextcloud, not on the frame. */
#define USE_MONITOR 0
#define USE_SDL_GPU 0
#define USE_WAYLAND 0
#define USE_XKB 0
#define USE_EVDEV 0
#define USE_LIBINPUT 0

#endif /*LV_DRV_CONF_H*/
