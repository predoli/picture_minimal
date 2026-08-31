#include "ipc_client.hpp"

#include <nlohmann/json.hpp>

#include <cerrno>
#include <cstring>
#include <iostream>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

namespace frame {
namespace {

/** RAII wrapper so every early return closes the socket exactly once. */
class Socket {
public:
    explicit Socket(int fd) noexcept : m_fd{fd} {}
    ~Socket()
    {
        if (m_fd >= 0) {
            ::close(m_fd);
        }
    }
    Socket(const Socket&) = delete;
    Socket& operator=(const Socket&) = delete;

    int get() const noexcept { return m_fd; }
    bool valid() const noexcept { return m_fd >= 0; }

private:
    int m_fd;
};

bool set_timeout(int fd, int option, std::chrono::milliseconds timeout)
{
    struct timeval tv {};
    tv.tv_sec = static_cast<decltype(tv.tv_sec)>(timeout.count() / 1000);
    tv.tv_usec = static_cast<decltype(tv.tv_usec)>((timeout.count() % 1000) * 1000);
    return ::setsockopt(fd, SOL_SOCKET, option, &tv, sizeof(tv)) == 0;
}

bool write_all(int fd, const std::string& data)
{
    size_t sent = 0;
    while (sent < data.size()) {
        const ssize_t n = ::write(fd, data.data() + sent, data.size() - sent);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) {
                continue;
            }
            return false;
        }
        sent += static_cast<size_t>(n);
    }
    return true;
}

/**
 * Read until the newline that terminates one NDJSON message.
 *
 * The previous implementation read once into a fixed 256-byte buffer, which
 * silently truncated any longer reply and had no framing at all.
 */
std::optional<std::string> read_line(int fd)
{
    std::string line;
    char buffer[512];
    while (line.size() < 64 * 1024) {
        const ssize_t n = ::read(fd, buffer, sizeof(buffer));
        if (n < 0) {
            if (errno == EINTR) {
                continue;
            }
            return std::nullopt; // includes EAGAIN from SO_RCVTIMEO
        }
        if (n == 0) {
            break; // peer closed
        }
        line.append(buffer, static_cast<size_t>(n));
        const auto newline = line.find('\n');
        if (newline != std::string::npos) {
            line.resize(newline);
            return line;
        }
    }
    return line.empty() ? std::nullopt : std::optional<std::string>{line};
}

std::optional<Reply> parse_reply(const std::string& text)
{
    const auto json = nlohmann::json::parse(text, nullptr, /*allow_exceptions=*/false);
    if (json.is_discarded() || !json.is_object()) {
        std::cerr << "[ipc] malformed reply: " << text << '\n';
        return std::nullopt;
    }

    if (json.contains("error")) {
        std::cerr << "[ipc] backend error: " << json.value("error", "") << '\n';
        return std::nullopt;
    }

    const auto mode = json.value("mode", std::string{});
    if (mode == "pairing") {
        return Reply{PairingRequired{json.value("url", ""), json.value("host", "")}};
    }
    if (mode == "empty") {
        return Reply{NothingToShow{json.value("message", "")}};
    }

    ImageReady image;
    image.id = json.value("id", "");
    image.blob = json.value("blob", "");
    image.width = json.value("w", 0);
    image.height = json.value("h", 0);
    image.stride = json.value("stride", 0);
    image.name = json.value("name", "");

    if (image.blob.empty() || image.width <= 0 || image.height <= 0 || image.stride <= 0) {
        std::cerr << "[ipc] incomplete image reply: " << text << '\n';
        return std::nullopt;
    }
    return Reply{std::move(image)};
}

} // namespace

IpcClient::IpcClient(std::filesystem::path socket_path, std::chrono::milliseconds io_timeout)
    : m_socket_path{std::move(socket_path)}, m_io_timeout{io_timeout}
{
    m_worker = std::thread(&IpcClient::worker_loop, this);
}

IpcClient::~IpcClient()
{
    {
        std::lock_guard lock(m_mutex);
        m_stop = true;
    }
    m_cv.notify_all();
    if (m_worker.joinable()) {
        m_worker.join();
    }
}

void IpcClient::worker_loop()
{
    for (;;) {
        std::function<void()> task;
        {
            std::unique_lock lock(m_mutex);
            m_cv.wait(lock, [this] { return m_stop || !m_tasks.empty(); });
            if (m_tasks.empty()) {
                // Only reachable when stopping; drain first so no promise is
                // abandoned and no waiter sees a broken_promise.
                return;
            }
            task = std::move(m_tasks.front());
            m_tasks.pop();
        }
        task();
    }
}

std::future<std::optional<Reply>> IpcClient::request(int width, int height, const std::string& last)
{
    auto task = std::make_shared<std::packaged_task<std::optional<Reply>()>>(
        [this, width, height, last] { return exchange(width, height, last); });

    auto future = task->get_future();
    {
        std::lock_guard lock(m_mutex);
        if (m_stop) {
            return future; // task dropped; caller sees the promise break during shutdown
        }
        m_tasks.emplace([task] { (*task)(); });
    }
    m_cv.notify_one();
    return future;
}

std::optional<Reply> IpcClient::exchange(int width, int height, const std::string& last)
{
    Socket sock{::socket(AF_UNIX, SOCK_STREAM, 0)};
    if (!sock.valid()) {
        std::cerr << "[ipc] socket(): " << std::strerror(errno) << '\n';
        return std::nullopt;
    }

    // Without these a backend that accepts but never answers would stall the
    // render loop for as long as it stayed up.
    set_timeout(sock.get(), SO_RCVTIMEO, m_io_timeout);
    set_timeout(sock.get(), SO_SNDTIMEO, m_io_timeout);

    struct sockaddr_un addr {};
    addr.sun_family = AF_UNIX;
    const std::string path = m_socket_path.string();
    if (path.size() >= sizeof(addr.sun_path)) {
        std::cerr << "[ipc] socket path too long: " << path << '\n';
        return std::nullopt;
    }
    std::memcpy(addr.sun_path, path.c_str(), path.size() + 1);

    if (::connect(sock.get(), reinterpret_cast<struct sockaddr*>(&addr), sizeof(addr)) != 0) {
        std::cerr << "[ipc] connect(" << path << "): " << std::strerror(errno) << '\n';
        return std::nullopt;
    }

    const nlohmann::json request{
        {"w", width}, {"h", height}, {"format", "rgb565"}, {"last", last}};
    if (!write_all(sock.get(), request.dump() + "\n")) {
        std::cerr << "[ipc] write: " << std::strerror(errno) << '\n';
        return std::nullopt;
    }
    ::shutdown(sock.get(), SHUT_WR);

    const auto line = read_line(sock.get());
    if (!line) {
        std::cerr << "[ipc] no reply within timeout\n";
        return std::nullopt;
    }
    return parse_reply(*line);
}

} // namespace frame
