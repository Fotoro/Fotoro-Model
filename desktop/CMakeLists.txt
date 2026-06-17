cmake_minimum_required(VERSION 3.16)
project(fotoro-desktop VERSION 1.0 LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

set(CMAKE_AUTOMOC ON)
set(CMAKE_AUTORCC ON)
set(CMAKE_AUTOUIC ON)

find_package(Qt6 REQUIRED COMPONENTS Core Widgets Network)

add_executable(fotoro main.cpp)
target_link_libraries(fotoro PRIVATE Qt6::Core Qt6::Widgets Qt6::Network)
