#pragma once

#include "frame_image.hpp"

#include <lvgl.h>

#include <memory>

namespace frame {

/**
 * Two stacked image layers that cross-fade between photos.
 *
 * All LVGL calls happen on the thread running lv_timer_handler(), which is the
 * main thread. Network and disk work lives on IpcClient's worker thread and
 * never touches LVGL, so no locking is needed here.
 */
class Slideshow {
public:
    Slideshow();

    Slideshow(const Slideshow&) = delete;
    Slideshow& operator=(const Slideshow&) = delete;

    /** Fade to a new image, taking ownership of its mapping. */
    void show(std::unique_ptr<FrameImage> image, uint32_t fade_ms);

    /** Hide both layers, e.g. while the pairing screen is up. */
    void set_visible(bool visible);

    bool fading() const noexcept { return m_fading; }

private:
    struct Layer {
        lv_obj_t* obj{nullptr};
        std::unique_ptr<FrameImage> image;
    };

    static void set_img_opa(void* obj, int32_t value);
    static void fade_done_cb(lv_anim_t* anim);
    void on_fade_done();

    Layer m_front; // currently visible
    Layer m_back;  // incoming, drawn on top while fading in
    bool m_fading{false};
};

} // namespace frame
