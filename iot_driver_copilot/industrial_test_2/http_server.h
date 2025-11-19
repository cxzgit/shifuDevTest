#ifndef HTTP_SERVER_H
#define HTTP_SERVER_H

#include <string>
#include <thread>
#include <atomic>
#include <functional>
#include <iostream>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <sys/socket.h>
#include <unistd.h>
#include <cstring>

class HttpServer {
public:
    using Handler = std::function<std::string(const std::string&)>;
    HttpServer(const std::string &host, int port, Handler handler)
        : host_(host), port_(port), handler_(handler), running_(false), srv_fd_(-1) {}

    bool start() {
        if (running_) return true;
        srv_fd_ = ::socket(AF_INET, SOCK_STREAM, 0);
        if (srv_fd_ < 0) {
            std::cerr << "socket failed" << std::endl;
            return false;
        }
        int opt = 1;
        setsockopt(srv_fd_, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
        struct sockaddr_in addr; memset(&addr, 0, sizeof(addr));
        addr.sin_family = AF_INET;
        addr.sin_port = htons(port_);
        if (inet_pton(AF_INET, host_.c_str(), &addr.sin_addr) <= 0) {
            std::cerr << "inet_pton failed for host " << host_ << std::endl;
            ::close(srv_fd_);
            srv_fd_ = -1;
            return false;
        }
        if (bind(srv_fd_, (struct sockaddr*)&addr, sizeof(addr)) < 0) {
            std::cerr << "bind failed" << std::endl;
            ::close(srv_fd_);
            srv_fd_ = -1;
            return false;
        }
        if (listen(srv_fd_, 16) < 0) {
            std::cerr << "listen failed" << std::endl;
            ::close(srv_fd_);
            srv_fd_ = -1;
            return false;
        }
        running_ = true;
        th_ = std::thread([this](){ this->run(); });
        return true;
    }

    void stop() {
        if (!running_) return;
        running_ = false;
        if (srv_fd_ >= 0) {
            ::shutdown(srv_fd_, SHUT_RDWR);
            ::close(srv_fd_);
            srv_fd_ = -1;
        }
        if (th_.joinable()) th_.join();
    }

private:
    void run() {
        while (running_) {
            struct sockaddr_in cli; socklen_t clilen = sizeof(cli);
            int cfd = ::accept(srv_fd_, (struct sockaddr*)&cli, &clilen);
            if (cfd < 0) {
                if (!running_) break;
                // transient error
                continue;
            }
            handle_client(cfd);
            ::close(cfd);
        }
    }

    void handle_client(int cfd) {
        char buf[2048];
        ssize_t n = ::read(cfd, buf, sizeof(buf)-1);
        if (n <= 0) return;
        buf[n] = '\0';
        std::string req(buf);
        std::string method;
        std::string path;
        parse_request_line(req, method, path);
        if (method != "GET") {
            send_response(cfd, 405, "Method Not Allowed", "{\"error\":\"method_not_allowed\"}");
            return;
        }
        if (path == "/status" || path == "/readings") {
            std::string body = handler_(path);
            if (body.empty()) {
                send_response(cfd, 500, "Internal Server Error", "{\"error\":\"empty_response\"}");
            } else {
                send_response(cfd, 200, "OK", body);
            }
        } else {
            send_response(cfd, 404, "Not Found", "{\"error\":\"not_found\"}");
        }
    }

    void parse_request_line(const std::string &req, std::string &method, std::string &path) {
        size_t pos = req.find("\r\n");
        std::string line = pos == std::string::npos ? req : req.substr(0, pos);
        size_t msp = line.find(' ');
        size_t psp = line.find(' ', msp == std::string::npos ? 0 : msp + 1);
        if (msp == std::string::npos || psp == std::string::npos) { method = ""; path = ""; return; }
        method = line.substr(0, msp);
        path = line.substr(msp + 1, psp - (msp + 1));
    }

    void send_response(int cfd, int code, const char *reason, const std::string &body) {
        std::string hdr = "HTTP/1.1 " + std::to_string(code) + " " + reason + "\r\n";
        hdr += "Content-Type: application/json\r\n";
        hdr += "Connection: close\r\n";
        hdr += "Content-Length: " + std::to_string(body.size()) + "\r\n\r\n";
        ::write(cfd, hdr.c_str(), hdr.size());
        ::write(cfd, body.c_str(), body.size());
    }

    std::string host_;
    int port_;
    Handler handler_;
    std::atomic<bool> running_;
    int srv_fd_;
    std::thread th_;
};

#endif // HTTP_SERVER_H
