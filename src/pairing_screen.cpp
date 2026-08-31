#include "pairing_screen.hpp"

#include <algorithm>

namespace frame {
namespace {

constexpr lv_coord_t kMinQrSize = 120;
constexpr lv_coord_t kMaxQrSize = 480;

lv_obj_t* create_label(lv_obj_t* parent, const lv_font_t* font, lv_coord_t max_width)
{
    lv_obj_t* label = lv_label_create(parent);
    lv_obj_set_style_text_color(label, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(label, font, LV_PART_MAIN);
    lv_obj_set_style_text_align(label, LV_TEXT_ALIGN_CENTER, LV_PART_MAIN);
    lv_label_set_long_mode(label, LV_LABEL_LONG_WRAP);
    lv_obj_set_width(label, max_width);
    return label;
}

} // namespace

PairingScreen::PairingScreen(lv_coord_t display_width, lv_coord_t display_height)
{
    const lv_coord_t text_width = static_cast<lv_coord_t>(display_width * 0.8);

    m_root = lv_obj_create(lv_scr_act());
    lv_obj_remove_style_all(m_root);
    lv_obj_set_size(m_root, display_width, display_height);
    lv_obj_set_style_bg_color(m_root, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(m_root, LV_OPA_COVER, LV_PART_MAIN);
    lv_obj_set_flex_flow(m_root, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_flex_align(m_root, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER,
                          LV_FLEX_ALIGN_CENTER);
    lv_obj_set_style_pad_row(m_root, 16, LV_PART_MAIN);
    lv_obj_clear_flag(m_root, LV_OBJ_FLAG_SCROLLABLE);

    m_title = create_label(m_root, &lv_font_montserrat_28, text_width);

    const lv_coord_t qr_size =
        std::clamp(static_cast<lv_coord_t>(std::min(display_width, display_height) / 2),
                   kMinQrSize, kMaxQrSize);
    m_qr = lv_qrcode_create(m_root, qr_size, lv_color_black(), lv_color_white());
    // QR codes need a light quiet zone to scan reliably; without padding the
    // black frame background runs straight into the code's outer modules.
    lv_obj_set_style_pad_all(m_qr, 12, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_qr, 0, LV_PART_MAIN);

    m_detail = create_label(m_root, &lv_font_montserrat_20, text_width);
    m_footer = create_label(m_root, &lv_font_montserrat_14, text_width);
    lv_obj_set_style_text_color(m_footer, lv_palette_main(LV_PALETTE_GREY), LV_PART_MAIN);

    set_visible(false);
}

void PairingScreen::show_pairing(const std::string& url, const std::string& host)
{
    lv_label_set_text(m_title, "Link this frame to Nextcloud");

    if (url != m_encoded_url) {
        lv_qrcode_update(m_qr, url.c_str(), static_cast<uint32_t>(url.size()));
        m_encoded_url = url;
    }
    lv_obj_clear_flag(m_qr, LV_OBJ_FLAG_HIDDEN);

    lv_label_set_text(m_detail, "Scan with your phone, then sign in and approve access.");
    lv_label_set_text_fmt(m_footer, "%s\n%s", host.c_str(), url.c_str());

    set_visible(true);
}

void PairingScreen::show_message(const std::string& text)
{
    lv_label_set_text(m_title, "Nothing to show yet");
    lv_obj_add_flag(m_qr, LV_OBJ_FLAG_HIDDEN);
    lv_label_set_text(m_detail, text.c_str());
    lv_label_set_text(m_footer, "");
    m_encoded_url.clear();

    set_visible(true);
}

void PairingScreen::hide()
{
    set_visible(false);
}

void PairingScreen::set_visible(bool visible)
{
    m_visible = visible;
    if (visible) {
        lv_obj_clear_flag(m_root, LV_OBJ_FLAG_HIDDEN);
        lv_obj_move_foreground(m_root);
    } else {
        lv_obj_add_flag(m_root, LV_OBJ_FLAG_HIDDEN);
    }
}

} // namespace frame
