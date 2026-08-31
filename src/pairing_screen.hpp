#pragma once

#include <lvgl.h>

#include <string>

namespace frame {

/**
 * The only part of the frontend that draws UI rather than blitting a photo.
 *
 * Two states share one layout: the pairing prompt (QR code plus the URL in text
 * for anyone whose camera will not cooperate) and a plain message used for the
 * empty state. Drawing this here rather than in the Go backend is deliberate —
 * LVGL already has font rendering, whereas producing legible text in the blob
 * pipeline would mean embedding a TTF.
 */
class PairingScreen {
public:
    PairingScreen(lv_coord_t display_width, lv_coord_t display_height);

    PairingScreen(const PairingScreen&) = delete;
    PairingScreen& operator=(const PairingScreen&) = delete;

    /** Show the QR for a Nextcloud Login Flow v2 URL. Cheap to call repeatedly. */
    void show_pairing(const std::string& url, const std::string& host);

    /** Show a text-only notice, such as "nothing is tagged yet". */
    void show_message(const std::string& text);

    void hide();

    bool visible() const noexcept { return m_visible; }

private:
    void set_visible(bool visible);

    lv_obj_t* m_root{nullptr};
    lv_obj_t* m_title{nullptr};
    lv_obj_t* m_qr{nullptr};
    lv_obj_t* m_detail{nullptr};
    lv_obj_t* m_footer{nullptr};

    // The backend re-polls every couple of seconds while unpaired, but the login
    // URL only changes when the 20-minute flow is regenerated. Re-encoding the
    // QR on every poll would burn CPU and make the code visibly flicker.
    std::string m_encoded_url;
    bool m_visible{false};
};

} // namespace frame
