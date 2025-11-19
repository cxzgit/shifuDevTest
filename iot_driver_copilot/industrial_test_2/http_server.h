#pragma once
#include <string>
#include <map>
#include <thread>
#include <atomic>

struct HttpRequest {
    std::string method;
    std::string path;
    std::map<std::string, std::string> headers;
    std::string body;
};

struct HttpResponse {
    int status{200};
    std::string status_text{"OK"};
    std::map<std::string, std::string> headers;
    std::string body;
};

class IHttpRequestHandler {
public:
    virtual ~IHttpRequestHandler() = default;
    virtual void handleRequest(const HttpRequest& req, HttpResponse& resp) = 0;
};

class HttpServer {
public:
    HttpServer(const std::string& host, int port, IHttpRequestHandler* handler);
    ~HttpServer();
    bool start();
    void stop();

private:
    std::string host_;
    int port_;
    int server_fd_;
    std::thread thread_;
    std::atomic<bool> running_;
    IHttpRequestHandler* handler_;

    void run();
};
