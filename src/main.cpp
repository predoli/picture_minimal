#include "display_backend.hpp"
#include "frame_image.hpp"
#include "ipc_client.hpp"
#include "pairing_screen.hpp"
#include "slideshow.hpp"

#include <lvgl.h>

#include <atomic>
#include <chrono>
#include <csignal>
#include <cstdlib>
#include <iostream>
#include <string>
#include <thread>

namespace {

std::atomic<bool> g_stop{false};

void handle_signal(int)
{
    g_stop.store(true);
}

std::string env_or(const char* name, const std::string& fallback)
{
    const char* value = std::getenv(name);
    return (value != nullptr && *value != '\0') ? std::string{value} : fallback;
}

std::chrono::milliseconds env_ms(const char* name, std::chrono::milliseconds fallback)
{
    const char* value = std::getenv(name);
    if (value == nullptr || *value == '\0') {
        return fallback;
    }
    try {
        const long parsed = std::stol(value);
        if (parsed > 0) {
            return std::chrono::milliseconds{parsed};
        }
    } catch (const std::exception&) {
        // fall through to the default below
    }
    std::cerr << "[frame] ignoring invalid " << name << "='" << value << "'\n";
    return fallback;
}

struct Settings {
    std::string socket_path = env_or("FRAME_SOCKET", "/run/picture-frame/frontend.sock");
    std::chrono::milliseconds interval = env_ms("FRAME_INTERVAL_MS", std::chrono::seconds{30});
    std::chrono::milliseconds fade = env_ms("FRAME_FADE_MS", std::chrono::seconds{1});

    // While unpaired or empty the backend's answer can change at any moment, so
    // poll briskly: the moment the user approves on their phone, the photos
    // should appear rather than waiting out a full slideshow interval.
    std::chrono::milliseconds pairing_poll = std::chrono::seconds{2};

    // A backend that is down should not turn into a busy loop.
    std::chrono::milliseconds retry = std::chrono::seconds{5};
};

} // namespace

int main()
{
    std::signal(SIGINT, handle_signal);
    std::signal(SIGTERM, handle_signal);

    // Under systemd, stdout is a pipe to journald and would otherwise be block
    // buffered, so log lines would appear in batches long after the event.
    std::cout << std::unitbuf;

    const Settings settings;

    lv_init();

    std::unique_ptr<frame::DisplayBackend> display;
    try {
        display = std::make_unique<frame::DisplayBackend>();
    } catch (const frame::DisplayError& error) {
        std::cerr << "[frame] display init failed: " << error.what() << '\n';
        return 1;
    }

    std::cout << "[frame] " << frame::DisplayBackend::name() << " display "
              << display->width() << "x" << display->height() << ", socket "
              << settings.socket_path << '\n';
    std::cout << "[frame] " << settings.interval.count() << " ms per photo, "
              << settings.fade.count() << " ms cross-fade, polling every "
              << settings.pairing_poll.count() << " ms while idle\n";

    frame::Slideshow slideshow;
    frame::PairingScreen pairing{display->width(), display->height()};
    frame::IpcClient client{settings.socket_path};

    std::string last_id;
    std::future<std::optional<frame::Reply>> pending;
    auto next_request = std::chrono::steady_clock::now();

    // The frame polls every couple of seconds while it is waiting for photos, so
    // the pairing URL and the "nothing to show" text would fill the log if they
    // were printed each time. Log them only when they change, and do the same
    // for losing and regaining the backend.
    std::string last_notice;
    bool backend_reachable = true;

    while (!g_stop.load()) {
        lv_timer_handler();

        const auto now = std::chrono::steady_clock::now();

        if (pending.valid()) {
            if (pending.wait_for(std::chrono::seconds{0}) == std::future_status::ready) {
                const auto reply = pending.get();
                pending = {};

                if (!reply.has_value()) {
                    // Backend unreachable or confused. Keep whatever is on screen
                    // rather than blanking the frame, and try again shortly.
                    if (backend_reachable) {
                        std::cerr << "[frame] backend not answering; keeping the current image\n";
                        backend_reachable = false;
                    }
                    next_request = now + settings.retry;
                } else if (const auto* image = std::get_if<frame::ImageReady>(&*reply)) {
                    if (!backend_reachable) {
                        std::cout << "[frame] backend is answering again\n";
                        backend_reachable = true;
                    }
                    try {
                        auto mapped = std::make_unique<frame::FrameImage>(
                            image->blob, image->width, image->height, image->stride);
                        pairing.hide();
                        slideshow.set_visible(true);
                        slideshow.show(std::move(mapped),
                                       static_cast<uint32_t>(settings.fade.count()));
                        last_id = image->id;
                        last_notice.clear();
                        std::cout << "[frame] showing " << image->name << " (" << image->width
                                  << "x" << image->height << ", " << image->blob << ")\n";
                        next_request = now + settings.interval;
                    } catch (const frame::ImageError& error) {
                        // One unreadable blob should not stall the rotation; ask
                        // for the next photo sooner than the normal interval.
                        std::cerr << "[frame] skipping " << image->name << ": " << error.what()
                                  << '\n';
                        last_id = image->id;
                        next_request = now + settings.retry;
                    }
                } else if (const auto* prompt = std::get_if<frame::PairingRequired>(&*reply)) {
                    backend_reachable = true;
                    slideshow.set_visible(false);
                    pairing.show_pairing(prompt->url, prompt->host);
                    if (last_notice != prompt->url) {
                        std::cout << "[frame] showing the pairing QR for " << prompt->host << ": "
                                  << prompt->url << '\n';
                        last_notice = prompt->url;
                    }
                    last_id.clear();
                    next_request = now + settings.pairing_poll;
                } else if (const auto* empty = std::get_if<frame::NothingToShow>(&*reply)) {
                    backend_reachable = true;
                    slideshow.set_visible(false);
                    pairing.show_message(empty->message);
                    if (last_notice != empty->message) {
                        std::cout << "[frame] nothing to show: " << empty->message << '\n';
                        last_notice = empty->message;
                    }
                    last_id.clear();
                    next_request = now + settings.pairing_poll;
                }
            }
        } else if (now >= next_request) {
            pending = client.request(display->width(), display->height(), last_id);
        }

        // LVGL's default refresh period is 30 ms; polling at 5 ms keeps fades
        // smooth without spinning the CPU.
        std::this_thread::sleep_for(std::chrono::milliseconds{5});
    }

    std::cout << "[frame] shutting down\n";
    return 0;
}
