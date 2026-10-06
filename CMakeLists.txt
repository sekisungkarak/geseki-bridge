cmake_minimum_required(VERSION 3.28...3.30)

include("${CMAKE_CURRENT_SOURCE_DIR}/cmake/common/bootstrap.cmake" NO_POLICY_SCOPE)

project(${_name} VERSION ${_version})

option(ENABLE_FRONTEND_API "Use obs-frontend-api for UI functionality" OFF)
option(ENABLE_QT "Use Qt functionality" OFF)

include(compilerconfig)
include(defaults)
include(helpers)

add_library(${CMAKE_PROJECT_NAME} MODULE)

find_package(libobs REQUIRED)
target_link_libraries(${CMAKE_PROJECT_NAME} PRIVATE OBS::libobs)

if(ENABLE_FRONTEND_API)
  find_package(obs-frontend-api REQUIRED)
  target_link_libraries(${CMAKE_PROJECT_NAME} PRIVATE OBS::obs-frontend-api)
endif()

# The settings UI is a native Qt dialog inside OBS. Qt itself is NOT bundled:
# OBS already ships it, so we only link against it (the CI pulls the matching
# obs-deps Qt6 package declared in buildspec.json).
if(ENABLE_QT)
  find_package(Qt6 COMPONENTS Widgets Core REQUIRED)
  target_link_libraries(${CMAKE_PROJECT_NAME} PRIVATE Qt6::Core Qt6::Widgets)
  target_compile_definitions(${CMAKE_PROJECT_NAME} PRIVATE GESEKI_HAS_QT)
  set_target_properties(${CMAKE_PROJECT_NAME} PROPERTIES AUTOMOC ON AUTOUIC ON AUTORCC ON)
endif()

target_sources(
  ${CMAKE_PROJECT_NAME}
  PRIVATE
    src/plugin-main.cpp
    src/bridge-server.cpp
    src/json-util.cpp
    src/smtc-winrt.cpp
    src/tiktok-supervisor.cpp
    src/widget-docks.cpp
    src/backup.cpp
)

if(ENABLE_QT)
  target_sources(${CMAKE_PROJECT_NAME} PRIVATE src/settings-dialog.cpp src/backup-dialog.cpp)
endif()

target_include_directories(${CMAKE_PROJECT_NAME} PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/src")

# Windows-only bits: Winsock + shell for the settings page, WinRT for SMTC.
# C++/WinRT is used in blocking mode (IAsyncOperation::get()), so the C++17
# standard set by cmake/common/compiler_common.cmake is sufficient — no
# coroutines and no C++20 requirement.
if(OS_WINDOWS)
  target_link_libraries(${CMAKE_PROJECT_NAME} PRIVATE ws2_32 shell32 windowsapp runtimeobject iphlpapi)
  target_compile_definitions(
    ${CMAKE_PROJECT_NAME} PRIVATE _WIN32_WINNT=0x0A00 NOMINMAX WIN32_LEAN_AND_MEAN
  )
  # The WinRT headers are large enough to blow past MSVC's default section limit
  # in a single translation unit.
  target_compile_options(${CMAKE_PROJECT_NAME} PRIVATE /bigobj)
endif()

set_target_properties_plugin(${CMAKE_PROJECT_NAME} PROPERTIES OUTPUT_NAME ${_name})
