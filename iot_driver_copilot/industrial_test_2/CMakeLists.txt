cmake_minimum_required(VERSION 3.12)
project(env_modbus_driver LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

add_executable(driver driver.cpp)

find_package(Threads REQUIRED)
target_link_libraries(driver PRIVATE Threads::Threads)

target_compile_options(driver PRIVATE -O2 -Wall -Wextra -Wpedantic)
