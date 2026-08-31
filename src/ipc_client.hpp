#pragma once

#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <filesystem>
#include <functional>
#include <future>
#include <mutex>
#include <optional>
#include <queue>
#include <string>
#include <thread>
#include <variant>

namespace frame {

/** The backend has an image ready, already rendered to the panel's geometry. */
struct ImageReady {
    std::string id;
    std::filesystem::path blob;
    int width{0};
    int height{0};
    int stride{0};
    std::string name;
};

/** The frame is not yet linked to a Nextcloud account; show the QR code. */
struct PairingRequired {
    std::string url;
    std::string host;
};

/** Paired, but nothing matches the configured selection yet. */
struct NothingToShow {
    std::string message;
};

using Reply = std::variant<ImageReady, PairingRequired, NothingToShow>;

/**
 * Asks the Go backend for the next thing to display, over a Unix socket.
 *
 * One short-lived connection per request, which keeps failure handling trivial:
 * a wedged or restarted backend can never leave this client holding a broken
 * long-lived socket. Requests are serviced on a dedicated worker thread so the
 * render loop never blocks on IO.
 */
class IpcClient {
public:
    explicit IpcClient(std::filesystem::path socket_path,
                       std::chrono::milliseconds io_timeout = std::chrono::seconds{5});
    ~IpcClient();

    IpcClient(const IpcClient&) = delete;
    IpcClient& operator=(const IpcClient&) = delete;

    /**
     * Queue a request for the next image. The frontend declares its own geometry,
     * so the backend always renders to the true panel size.
     *
     * @param last  id of the image currently on screen, so the backend can avoid
     *              repeating it; empty on first call.
     * @return future holding the reply, or nullopt if the exchange failed.
     */
    std::future<std::optional<Reply>> request(int width, int height, const std::string& last);

private:
    void worker_loop();
    std::optional<Reply> exchange(int width, int height, const std::string& last);

    const std::filesystem::path m_socket_path;
    const std::chrono::milliseconds m_io_timeout;

    std::mutex m_mutex;
    std::condition_variable m_cv;
    std::queue<std::function<void()>> m_tasks;
    bool m_stop{false};

    // Declared last so the thread starts only once every member it touches is
    // fully constructed.
    std::thread m_worker;
};

} // namespace frame
