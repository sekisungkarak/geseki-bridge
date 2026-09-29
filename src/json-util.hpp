#pragma once

#include <cstdint>
#include <string>
#include <utility>
#include <vector>

// Minimal JSON helpers.
//
// The plugin emits a handful of well-known shapes (docs/protocol.md) and parses
// only small `cmd` messages and the sidecar's state frames, so a full JSON
// library would be dead weight inside an OBS module.
namespace geseki::json {

// Escapes a UTF-8 string for use inside a JSON string literal. Control
// characters below 0x20 become \u00XX; quote and backslash are escaped.
std::string Escape(const std::string &in);

// Convenience: "key":"value" with the value escaped.
std::string Str(const std::string &key, const std::string &value);

// Convenience: "key":<number>.
std::string Num(const std::string &key, int64_t value);
std::string Num(const std::string &key, double value);

// Convenience: "key":true|false.
std::string Bool(const std::string &key, bool value);

// A parsed JSON document.
//
// Object keys keep their insertion order (the plugin only ever looks keys up by
// name, but stable output makes logs and diffs readable).
class Value {
public:
	enum class Type { Null, Bool, Number, String, Array, Object };

	Type type = Type::Null;
	bool boolean = false;
	double number = 0.0;
	std::string text;
	std::vector<Value> items;                          // array
	std::vector<std::pair<std::string, Value>> members; // object

	bool is_null() const { return type == Type::Null; }
	bool is_bool() const { return type == Type::Bool; }
	bool is_number() const { return type == Type::Number; }
	bool is_string() const { return type == Type::String; }
	bool is_array() const { return type == Type::Array; }
	bool is_object() const { return type == Type::Object; }

	// Object lookup; nullptr when absent or when this value is not an object.
	const Value *find(const std::string &key) const
	{
		if (type != Type::Object)
			return nullptr;
		for (const auto &kv : members)
			if (kv.first == key)
				return &kv.second;
		return nullptr;
	}

	std::string as_string(const std::string &fallback = std::string()) const
	{
		return type == Type::String ? text : fallback;
	}
	double as_number(double fallback = 0.0) const
	{
		return type == Type::Number ? number : fallback;
	}
	int64_t as_int(int64_t fallback = 0) const
	{
		return type == Type::Number ? static_cast<int64_t>(number) : fallback;
	}
	bool as_bool(bool fallback = false) const
	{
		return type == Type::Bool ? boolean : fallback;
	}

	// Parses one complete JSON document. Returns false and fills `error` on
	// malformed input; `out` is then unspecified.
	static bool Parse(const std::string &input, Value &out, std::string *error = nullptr);
};

// Renders a value back to compact JSON.
std::string Serialize(const Value &value);

} // namespace geseki::json
