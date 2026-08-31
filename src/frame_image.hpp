#pragma once

#include <lvgl.h>

#include <cstddef>
#include <filesystem>
#include <stdexcept>

namespace frame {

class ImageError : public std::runtime_error {
public:
    using std::runtime_error::runtime_error;
};

/**
 * A pre-rendered RGB565 blob mapped straight into an LVGL image descriptor.
 *
 * The backend has already decoded, oriented, and letterboxed the photo to the
 * panel's exact geometry, so there is nothing left to do but point LVGL at the
 * pixels. No decoding, no copying, and no full-resolution bitmap ever exists in
 * this process.
 *
 * Lifetime is the subtle part: LVGL keeps the pointer for as long as the image
 * is assigned to an object or held in its cache, so a mapping must not be
 * released until the object is detached and the cache entry invalidated. That is
 * what release() does, and why Display defers it until a fade has finished.
 */
class FrameImage {
public:
    FrameImage(const std::filesystem::path& blob, int width, int height, int stride);
    ~FrameImage();

    FrameImage(const FrameImage&) = delete;
    FrameImage& operator=(const FrameImage&) = delete;

    const lv_img_dsc_t* descriptor() const noexcept { return &m_dsc; }

private:
    void* m_data{nullptr};
    std::size_t m_size{0};
    lv_img_dsc_t m_dsc{};
};

} // namespace frame
