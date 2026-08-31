#pragma once

#include <lvgl.h>

#include <memory>
#include <stdexcept>
#include <vector>

namespace frame {

/**
 * Owns the LVGL display driver and its draw buffer.
 *
 * Two implementations exist behind one compile-time switch (FRAME_DISPLAY):
 * DRM/fbdev on the Pi, SDL2 on the development Mac. The distinction is confined
 * to display_backend.cpp; nothing else in the frontend knows which is active.
 *
 * The lv_disp_drv_t and lv_disp_draw_buf_t must outlive the display, so they are
 * members rather than locals.
 */
class DisplayBackend {
public:
    DisplayBackend();
    ~DisplayBackend();

    DisplayBackend(const DisplayBackend&) = delete;
    DisplayBackend& operator=(const DisplayBackend&) = delete;

    lv_coord_t width() const noexcept { return m_width; }
    lv_coord_t height() const noexcept { return m_height; }

    /** Human-readable backend name, for logs. */
    static const char* name() noexcept;

private:
    lv_coord_t m_width{0};
    lv_coord_t m_height{0};

    // One full-screen buffer. Sizing it to the whole panel keeps each refresh to
    // a single flush; a partial buffer would make the DRM driver memcpy the
    // entire framebuffer once per flush, which is ruinous during a full-screen
    // cross-fade on a Pi Zero.
    std::vector<lv_color_t> m_draw_pixels;
    lv_disp_draw_buf_t m_draw_buf{};
    lv_disp_drv_t m_disp_drv{};
    lv_disp_t* m_disp{nullptr};
};

/** Thrown when the panel cannot be opened or reports no usable geometry. */
class DisplayError : public std::runtime_error {
public:
    using std::runtime_error::runtime_error;
};

} // namespace frame
