#include "display_backend.hpp"

#include <string>

#if defined(FRAME_DISPLAY_DRM)
#include "display/drm.h"
#elif defined(FRAME_DISPLAY_FBDEV)
#include "display/fbdev.h"
#elif defined(FRAME_DISPLAY_SDL)
#include "sdl/sdl.h"
#else
#error "No display backend selected"
#endif

namespace frame {

const char* DisplayBackend::name() noexcept
{
#if defined(FRAME_DISPLAY_DRM)
    return "drm";
#elif defined(FRAME_DISPLAY_FBDEV)
    return "fbdev";
#else
    return "sdl";
#endif
}

DisplayBackend::DisplayBackend()
{
#if defined(FRAME_DISPLAY_DRM)
    drm_init();
    drm_get_sizes(&m_width, &m_height, nullptr);
    const auto flush_cb = drm_flush;
#elif defined(FRAME_DISPLAY_FBDEV)
    fbdev_init();
    uint32_t w = 0;
    uint32_t h = 0;
    fbdev_get_sizes(&w, &h, nullptr);
    m_width = static_cast<lv_coord_t>(w);
    m_height = static_cast<lv_coord_t>(h);
    const auto flush_cb = fbdev_flush;
#else
    sdl_init();
    m_width = SDL_HOR_RES;
    m_height = SDL_VER_RES;
    const auto flush_cb = sdl_display_flush;
#endif

    if (m_width <= 0 || m_height <= 0) {
        throw DisplayError(std::string("display backend '") + name() +
                           "' reported no usable geometry; is a panel connected?");
    }

    m_draw_pixels.resize(static_cast<size_t>(m_width) * static_cast<size_t>(m_height));
    lv_disp_draw_buf_init(&m_draw_buf, m_draw_pixels.data(), nullptr, m_draw_pixels.size());

    lv_disp_drv_init(&m_disp_drv);
    m_disp_drv.draw_buf = &m_draw_buf;
    m_disp_drv.flush_cb = flush_cb;
    m_disp_drv.hor_res = m_width;
    m_disp_drv.ver_res = m_height;
    m_disp_drv.full_refresh = 1; // one flush per refresh, matching the full-screen buffer

    m_disp = lv_disp_drv_register(&m_disp_drv);
    if (m_disp == nullptr) {
        throw DisplayError("lv_disp_drv_register failed");
    }
}

DisplayBackend::~DisplayBackend()
{
#if defined(FRAME_DISPLAY_DRM)
    drm_exit();
#endif
}

} // namespace frame
