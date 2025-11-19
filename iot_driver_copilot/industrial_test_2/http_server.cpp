#include "http_server.h"
#include <sys/types.h>
#include <sys/socket.h>
#include <netdb.h>
#include <unistd.h>
#include <cstring>
#include <sstream>
#include <iostream>

HttpServer::HttpServer(const std::string& host, int port, IHttpRequestHandler* handler)
: host_(host), port_(port), server_fd_(-1), running_(false), handler_(handler) {}

HttpServer::~HttpServer() {
    stop();
}

bool HttpServer::start() {
    if (running_) return true;

    struct addrinfo hints{};
    struct addrinfo* res = nullptr;
    hints.ai_family = AF_UNSPEC;
    hints.ai_socktype = SOCK_STREAM;
    hints.ai_flags = AI_PASSIVE;

    std::string portstr = std::to_string(port_);
    int err = getaddrinfo(host_.empty() ? nullptr : host_.c_str(), portstr.c_str(), &hints, &res);
    if (err != 0) {
        return false;
    }

    int fd = -1;
    for (struct addrinfo* p = res; p; p = p->ai_next) {
        fd = socket(p->ai_family, p->ai_socktype, p->ai_protocol);
        if (fd < 0) continue;
        int opt = 1;
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
        if (bind(fd, p->ai_addr, p->ai_addrlen) == 0) {
            if (listen(fd, 16) == 0) {
                server_fd_ = fd;
                break;
            }
        }
        close(fd);
        fd = -1;
    }
    freeaddrinfo(res);

    if (server_fd_ < 0) return false;

    running_.store(true);
    thread_ = std::thread(&HttpServer::run, this);
    return true;
}

void HttpServer::stop() {
    if (!running_.load()) return;
    running_.store(false);
    if (server_fd_ >= 0) {
        shutdown(server_fd_, SHUT_RDWR);
        close(server_fd_);
        server_fd_ = -1;
    }
    if (thread_.joinable()) thread_.join();
}

static bool parse_request_line(const std::string& line, HttpRequest& req) {
    std::istringstream iss(line);
    if (!(iss >> req.method)) return false;
    if (!(iss >> req.path)) return false;
    std::string httpver;
    if (!(iss >> httpver)) return false;
    return true;
}

static bool recv_until(int fd, std::string& out, const std::string& delim, size_t max_bytes = 8192) {
    char buf[1024];
    while (out.find(delim) == std::string::npos) {
        ssize_t r = recv(fd, buf, sizeof(buf), 0);
        if (r <= 0) return false;
        out.append(buf, buf + r);
        if (out.size() > max_bytes) return false;
    }
    return true;
}

static void send_all(int fd, const std::string& data) {
    size_t sent = 0;
    while (sent < data.size()) {
        ssize_t w = send(fd, data.data() + sent, data.size() - sent, 0);
        if (w <= 0) return;
        sent += (size_t)w;
    }
}

void HttpServer::run() {
    while (running_.load()) {
        int client_fd = accept(server_fd_, nullptr, nullptr);
        if (client_fd < 0) {
            if (!running_.load()) break;
            continue;
        }

        std::thread([this, client_fd]() {
            std::string data;
            if (!recv_until(client_fd, data, "\r\n\r\n")) {
                close(client_fd);
                return;
            }
            // Parse
            HttpRequest req;
            HttpResponse resp;

            size_t pos = data.find("\r\n");
            if (pos == std::string::npos) {
                resp.status = 400; resp.status_text = "Bad Request"; resp.body = "{""error"":""bad request""}"; resp.headers["Content-Type"] = "application/json";
            } else {
                std::string reqline = data.substr(0, pos);
                if (!parse_request_line(reqline, req)) {
                    resp.status = 400; resp.status_text = "Bad Request"; resp.body = "{""error"":""bad request""}"; resp.headers["Content-Type"] = "application/json";
                } else {
                    // headers (ignored mostly)
                    size_t cur = pos + 2;
                    while (true) {
                        size_t next = data.find("\r\n", cur);
                        if (next == std::string::npos || next == cur) break;
                        std::string hline = data.substr(cur, next - cur);
                        size_t colon = hline.find(":");
                        if (colon != std::string::npos) {
                            std::string key = hline.substr(0, colon);
                            std::string val = hline.substr(colon + 1);
                            // trim spaces
                            while (!val.empty() && (val.front() == ' ' || val.front() == '\t')) val.erase(val.begin());
                            req.headers[key] = val;
                        }
                        cur = next + 2;
                    }

                    if (handler_) handler_->handleRequest(req, resp);
                }
            }

            std::ostringstream oss;
            oss << "HTTP/1.1 " << resp.status << " " << resp.status_text << "\r\n";
            if (resp.headers.find("Content-Type") == resp.headers.end()) {
                resp.headers["Content-Type"] = "text/plain";
            }
            resp.headers["Content-Length"] = std::to_string(resp.body.size());
            resp.headers["Connection"] = "close";
            for (const auto& kv : resp.headers) {
                oss << kv.first << ": " << kv.second << "\r\n";
            }
            oss << "\r\n";
            oss << resp.body;

            std::string out = oss.str();
            send_all(client_fd, out);
            close(client_fd);
        }).detach();
    }
}
