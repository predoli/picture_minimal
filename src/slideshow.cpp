#include "slideshow.hpp"

#include <utility>

namespace frame {
namespace {

/**
 * A 1x1 image that outlives everything, used to detach a layer from a mapping.
 *
 * lv_img_set_src(obj, nullptr) looks like the obvious way to release an image,
 * but LVGL 8.3 treats a null source as an error: it warns on every call and then
 * warns again on every redraw, because the object stays in the tree with a
 * source it cannot classify. Pointing at a valid, permanently-live descriptor
 * detaches the mapping cleanly instead.
 */
const lv_img_dsc_t& blank_image()
{
    static const uint8_t pixels[sizeof(lv_color_t)] = {};
    static const lv_img_dsc_t dsc = [] {
        lv_img_dsc_t d{};
        d.header.cf = LV_IMG_CF_TRUE_COLOR;
        d.header.always_zero = 0;
        d.header.w = 1;
        d.header.h = 1;
        d.data_size = sizeof(pixels);
        d.data = pixels;
        return d;
    }();
    return dsc;
}

lv_obj_t* create_layer(lv_obj_t* parent)
{
    lv_obj_t* img = lv_img_create(parent);
    lv_obj_remove_style_all(img);
    // Start from the blank descriptor rather than a null source, so the very
    // first refresh has something valid to classify.
    lv_img_set_src(img, &blank_image());
    lv_obj_center(img);
    lv_obj_set_style_img_opa(img, LV_OPA_TRANSP, LV_PART_MAIN);
    return img;
}

} // namespace

Slideshow::Slideshow()
{
    lv_obj_t* screen = lv_scr_act();
    lv_obj_set_style_bg_color(screen, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(screen, LV_OPA_COVER, LV_PART_MAIN);

    m_front.obj = create_layer(screen);
    m_back.obj = create_layer(screen);
}

void Slideshow::set_img_opa(void* obj, int32_t value)
{
    // A real two-argument callback. lv_obj_set_style_img_opa takes three
    // parameters, so casting it to lv_anim_exec_xcb_t (as the original sketch
    // did) is undefined behaviour, not merely untidy.
    lv_obj_set_style_img_opa(static_cast<lv_obj_t*>(obj), static_cast<lv_opa_t>(value),
                             LV_PART_MAIN);
}

void Slideshow::fade_done_cb(lv_anim_t* anim)
{
    static_cast<Slideshow*>(anim->user_data)->on_fade_done();
}

void Slideshow::show(std::unique_ptr<FrameImage> image, uint32_t fade_ms)
{
    if (image == nullptr) {
        return;
    }

    // If a fade is still running, settle it first so the layers are in a known
    // state rather than half-way through a transition.
    if (m_fading) {
        lv_anim_del(m_back.obj, set_img_opa);
        lv_anim_del(m_front.obj, set_img_opa);
        on_fade_done();
    }

    m_back.image = std::move(image);
    lv_img_set_src(m_back.obj, m_back.image->descriptor());
    lv_obj_center(m_back.obj);
    lv_obj_set_style_img_opa(m_back.obj, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_move_foreground(m_back.obj);

    m_fading = true;

    lv_anim_t fade_in;
    lv_anim_init(&fade_in);
    lv_anim_set_var(&fade_in, m_back.obj);
    lv_anim_set_exec_cb(&fade_in, set_img_opa);
    lv_anim_set_values(&fade_in, LV_OPA_TRANSP, LV_OPA_COVER);
    lv_anim_set_time(&fade_in, fade_ms);
    fade_in.user_data = this;
    lv_anim_set_ready_cb(&fade_in, fade_done_cb);
    lv_anim_start(&fade_in);
}

void Slideshow::on_fade_done()
{
    m_fading = false;

    std::swap(m_front, m_back);

    // m_back is now the photo that was just faded out from under the new one.
    // Detach it before releasing the mapping: FrameImage's destructor evicts the
    // LVGL cache entry, but the object must not still name it as its source.
    lv_img_set_src(m_back.obj, &blank_image());
    lv_obj_set_style_img_opa(m_back.obj, LV_OPA_TRANSP, LV_PART_MAIN);
    m_back.image.reset();

    lv_obj_move_foreground(m_front.obj);
}

void Slideshow::set_visible(bool visible)
{
    for (lv_obj_t* obj : {m_front.obj, m_back.obj}) {
        if (visible) {
            lv_obj_clear_flag(obj, LV_OBJ_FLAG_HIDDEN);
        } else {
            lv_obj_add_flag(obj, LV_OBJ_FLAG_HIDDEN);
        }
    }
}

} // namespace frame
