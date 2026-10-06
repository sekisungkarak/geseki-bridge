#pragma once

#define PLUGIN_NAME "geseki-bridge"
#define PLUGIN_VERSION "0.6.0"
#define GESEKI_BRIDGE_VERSION "0.6.0"

// Wire protocol revision shared with the widgets. Bump only on a breaking
// change; see docs/protocol.md.
#define GESEKI_BRIDGE_PROTOCOL 1

// obs_log is NOT part of libobs: it comes from obs-plugintemplate's
// plugin-support.c, which this repo does not vendor. Map it onto libobs' own
// blog() instead.
//
// Deliberately a macro, and deliberately not including <obs.h> here: this
// header is included by bridge-server.cpp *before* <winsock2.h>, and pulling
// the OBS headers in at that point would drag in <windows.h> first and break
// the winsock2-before-windows include order. A macro is expanded at the call
// site, by which time <obs-module.h> has already declared blog()/LOG_*.
//
// The format string is part of __VA_ARGS__ rather than a named parameter so
// that the prefix literal concatenates with it and no trailing-comma
// extension (##__VA_ARGS__) is needed. Every call site passes a string
// literal as the format.
#define obs_log(level, ...) blog(level, "[" PLUGIN_NAME "] " __VA_ARGS__)
