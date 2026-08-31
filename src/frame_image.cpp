#include "frame_image.hpp"

#include <cerrno>
#include <cstring>
#include <fcntl.h>
#include <string>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

namespace frame {
namespace {

std::string describe(const std::filesystem::path& path, const char* what)
{
    return std::string{what} + " (" + path.string() + "): " + std::strerror(errno);
}

} // namespace

FrameImage::FrameImage(const std::filesystem::path& blob, int width, int height, int stride)
{
    if (width <= 0 || height <= 0) {
        throw ImageError("non-positive image geometry");
    }

    // LVGL's true-colour format assumes tightly packed rows. A padded stride
    // would be read as a diagonal smear rather than an image, so reject it here
    // instead of rendering garbage.
    const int expected_stride = width * static_cast<int>(sizeof(lv_color_t));
    if (stride != expected_stride) {
        throw ImageError("stride " + std::to_string(stride) + " does not match packed rows of " +
                         std::to_string(expected_stride) + " bytes");
    }

    m_size = static_cast<std::size_t>(stride) * static_cast<std::size_t>(height);

    const int fd = ::open(blob.c_str(), O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        throw ImageError(describe(blob, "open"));
    }

    struct stat st {};
    if (::fstat(fd, &st) != 0) {
        const auto message = describe(blob, "fstat");
        ::close(fd);
        throw ImageError(message);
    }
    if (static_cast<std::size_t>(st.st_size) < m_size) {
        ::close(fd);
        throw ImageError("blob " + blob.string() + " is " + std::to_string(st.st_size) +
                         " bytes, expected at least " + std::to_string(m_size));
    }

    m_data = ::mmap(nullptr, m_size, PROT_READ, MAP_PRIVATE, fd, 0);
    ::close(fd); // the mapping keeps its own reference
    if (m_data == MAP_FAILED) {
        m_data = nullptr;
        throw ImageError(describe(blob, "mmap"));
    }

    // Pull the pages in now. Faulting 4 MB off an SD card lazily, in the middle
    // of a cross-fade, is exactly when the frame can least afford to stall.
    ::madvise(m_data, m_size, MADV_WILLNEED);

    m_dsc.header.cf = LV_IMG_CF_TRUE_COLOR;
    m_dsc.header.always_zero = 0;
    m_dsc.header.w = static_cast<uint32_t>(width);
    m_dsc.header.h = static_cast<uint32_t>(height);
    m_dsc.data_size = static_cast<uint32_t>(m_size);
    m_dsc.data = static_cast<const uint8_t*>(m_data);
}

FrameImage::~FrameImage()
{
    if (m_data == nullptr) {
        return;
    }
    // LVGL caches decoded images by source pointer. Dropping the mapping while
    // an entry still refers to it would leave the cache pointing at unmapped
    // pages, so evict first.
    lv_img_cache_invalidate_src(&m_dsc);
    ::munmap(m_data, m_size);
    m_data = nullptr;
}

} // namespace frame
