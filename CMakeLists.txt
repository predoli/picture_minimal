cmake_minimum_required(VERSION 3.16)

project(PictureMinimal LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_C_STANDARD 11)
set(CMAKE_EXPORT_COMPILE_COMMANDS ON)

# Display backend: drm/fbdev on the Pi, sdl on the development Mac.
set(FRAME_DISPLAY "sdl" CACHE STRING "Display backend: drm, fbdev or sdl")
set_property(CACHE FRAME_DISPLAY PROPERTY STRINGS drm fbdev sdl)

# SDL's window size is fixed at compile time, unlike DRM which reports the real
# panel geometry at runtime. Override to rehearse against a frame's resolution.
set(FRAME_SDL_WIDTH 1024 CACHE STRING "SDL window width (sdl backend only)")
set(FRAME_SDL_HEIGHT 600 CACHE STRING "SDL window height (sdl backend only)")

# --------------------------------------------------------------------------
# Configuration headers
#
# include/ must precede libs/lvgl on the include path so our lv_conf.h wins over
# the upstream template, which is inert (wrapped in #if 0) and would leave LVGL
# on defaults with the QR widget and 16-bit colour switched off.
# --------------------------------------------------------------------------
add_library(frame_config INTERFACE)
target_include_directories(frame_config INTERFACE
    ${CMAKE_CURRENT_SOURCE_DIR}/include
    ${CMAKE_CURRENT_SOURCE_DIR}/libs
    ${CMAKE_CURRENT_SOURCE_DIR}/libs/json
)
target_compile_definitions(frame_config INTERFACE
    LV_CONF_INCLUDE_SIMPLE
    LV_LVGL_H_INCLUDE_SIMPLE
)

# LVGL declares its GC roots (lv_gc.c) as tentative definitions. Under the
# -fcommon default of older Apple clang those become common symbols, which the
# linker will not extract from a static archive — every root then shows up
# undefined at link time. GCC 10+ and clang 15+ already behave this way.
target_compile_options(frame_config INTERFACE -fno-common)

if(FRAME_DISPLAY STREQUAL "drm")
    target_compile_definitions(frame_config INTERFACE FRAME_DISPLAY_DRM)
elseif(FRAME_DISPLAY STREQUAL "fbdev")
    target_compile_definitions(frame_config INTERFACE FRAME_DISPLAY_FBDEV)
elseif(FRAME_DISPLAY STREQUAL "sdl")
    target_compile_definitions(frame_config INTERFACE
        FRAME_DISPLAY_SDL
        FRAME_SDL_WIDTH=${FRAME_SDL_WIDTH}
        FRAME_SDL_HEIGHT=${FRAME_SDL_HEIGHT}
    )
else()
    message(FATAL_ERROR "FRAME_DISPLAY must be drm, fbdev or sdl (got '${FRAME_DISPLAY}')")
endif()

# --------------------------------------------------------------------------
# LVGL
# --------------------------------------------------------------------------
add_subdirectory(libs/lvgl)
target_link_libraries(lvgl PUBLIC frame_config)

# --------------------------------------------------------------------------
# lv_drivers: only the one backend actually in use, rather than the whole set.
# --------------------------------------------------------------------------
if(FRAME_DISPLAY STREQUAL "drm")
    set(FRAME_DRIVER_SOURCES libs/lv_drivers/display/drm.c)
elseif(FRAME_DISPLAY STREQUAL "fbdev")
    set(FRAME_DRIVER_SOURCES libs/lv_drivers/display/fbdev.c)
else()
    set(FRAME_DRIVER_SOURCES
        libs/lv_drivers/sdl/sdl.c
        libs/lv_drivers/sdl/sdl_common.c
    )
endif()

add_library(frame_drivers STATIC ${FRAME_DRIVER_SOURCES})
target_include_directories(frame_drivers PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/libs/lv_drivers)
target_link_libraries(frame_drivers PUBLIC frame_config lvgl)

if(FRAME_DISPLAY STREQUAL "drm")
    find_package(PkgConfig REQUIRED)
    pkg_check_modules(LIBDRM REQUIRED IMPORTED_TARGET libdrm)
    target_link_libraries(frame_drivers PUBLIC PkgConfig::LIBDRM)
elseif(FRAME_DISPLAY STREQUAL "sdl")
    find_package(PkgConfig REQUIRED)
    pkg_check_modules(SDL2 REQUIRED IMPORTED_TARGET sdl2)
    target_link_libraries(frame_drivers PUBLIC PkgConfig::SDL2)
endif()

# --------------------------------------------------------------------------
# Frontend
# --------------------------------------------------------------------------
add_executable(PictureMinimal
    src/main.cpp
    src/frame_tick.cpp
    src/display_backend.cpp
    src/frame_image.cpp
    src/ipc_client.cpp
    src/pairing_screen.cpp
    src/slideshow.cpp
)

target_link_libraries(PictureMinimal PRIVATE frame_config lvgl frame_drivers)
target_compile_options(PictureMinimal PRIVATE -Wall -Wextra)

# The CMake target keeps its historical name, but the installed binary is called
# picture-frontend to match its systemd unit and its sibling picture-backend.
set_target_properties(PictureMinimal PROPERTIES OUTPUT_NAME picture-frontend)

find_package(Threads REQUIRED)
target_link_libraries(PictureMinimal PRIVATE Threads::Threads)

install(TARGETS PictureMinimal RUNTIME DESTINATION bin)
