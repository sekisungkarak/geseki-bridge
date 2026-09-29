#include "json-util.hpp"

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <sstream>

namespace geseki::json {

std::string Escape(const std::string &in)
{
	std::string out;
	out.reserve(in.size() + 8);
	for (unsigned char c : in) {
		switch (c) {
		case '"': out += "\\\""; break;
		case '\\': out += "\\\\"; break;
		case '\n': out += "\\n"; break;
		case '\r': out += "\\r"; break;
		case '\t': out += "\\t"; break;
		case '\b': out += "\\b"; break;
		case '\f': out += "\\f"; break;
		default:
			if (c < 0x20) {
				char buf[8];
				std::snprintf(buf, sizeof(buf), "\\u%04x", c);
				out += buf;
			} else {
				// Bytes >= 0x80 pass through: the input is already UTF-8 and
				// re-encoding would corrupt multi-byte sequences.
				out += static_cast<char>(c);
			}
		}
	}
	return out;
}

std::string Str(const std::string &key, const std::string &value)
{
	return "\"" + Escape(key) + "\":\"" + Escape(value) + "\"";
}

std::string Num(const std::string &key, int64_t value)
{
	return "\"" + Escape(key) + "\":" + std::to_string(value);
}

std::string Num(const std::string &key, double value)
{
	std::ostringstream ss;
	ss << value;
	return "\"" + Escape(key) + "\":" + ss.str();
}

std::string Bool(const std::string &key, bool value)
{
	return "\"" + Escape(key) + "\":" + (value ? "true" : "false");
}

// --------------------------------------------------------------------- parse

namespace {

class Parser {
public:
	Parser(const std::string &s) : s_(s) {}

	bool Run(Value &out)
	{
		SkipWs();
		if (!ParseValue(out))
			return false;
		SkipWs();
		if (i_ != s_.size()) {
			err_ = "trailing characters";
			return false;
		}
		return true;
	}

	const std::string &error() const { return err_; }

private:
	const std::string &s_;
	size_t i_ = 0;
	std::string err_;

	void SkipWs()
	{
		while (i_ < s_.size()) {
			char c = s_[i_];
			if (c == ' ' || c == '\t' || c == '\n' || c == '\r')
				++i_;
			else
				break;
		}
	}

	bool Fail(const std::string &m)
	{
		if (err_.empty())
			err_ = m;
		return false;
	}

	bool Literal(const char *word)
	{
		const size_t n = std::char_traits<char>::length(word);
		if (s_.compare(i_, n, word) != 0)
			return false;
		i_ += n;
		return true;
	}

	bool ParseValue(Value &out)
	{
		SkipWs();
		if (i_ >= s_.size())
			return Fail("unexpected end of input");
		const char c = s_[i_];
		switch (c) {
		case '{': return ParseObject(out);
		case '[': return ParseArray(out);
		case '"': {
			out.type = Value::Type::String;
			return ParseString(out.text);
		}
		case 't':
			if (!Literal("true"))
				return Fail("invalid literal");
			out.type = Value::Type::Bool;
			out.boolean = true;
			return true;
		case 'f':
			if (!Literal("false"))
				return Fail("invalid literal");
			out.type = Value::Type::Bool;
			out.boolean = false;
			return true;
		case 'n':
			if (!Literal("null"))
				return Fail("invalid literal");
			out.type = Value::Type::Null;
			return true;
		default:
			return ParseNumber(out);
		}
	}

	bool ParseObject(Value &out)
	{
		out.type = Value::Type::Object;
		++i_; // '{'
		SkipWs();
		if (i_ < s_.size() && s_[i_] == '}') {
			++i_;
			return true;
		}
		while (true) {
			SkipWs();
			if (i_ >= s_.size() || s_[i_] != '"')
				return Fail("expected object key");
			std::string key;
			if (!ParseString(key))
				return false;
			SkipWs();
			if (i_ >= s_.size() || s_[i_] != ':')
				return Fail("expected ':'");
			++i_;
			Value child;
			if (!ParseValue(child))
				return false;
			out.members.emplace_back(std::move(key), std::move(child));
			SkipWs();
			if (i_ >= s_.size())
				return Fail("unterminated object");
			if (s_[i_] == ',') {
				++i_;
				continue;
			}
			if (s_[i_] == '}') {
				++i_;
				return true;
			}
			return Fail("expected ',' or '}'");
		}
	}

	bool ParseArray(Value &out)
	{
		out.type = Value::Type::Array;
		++i_; // '['
		SkipWs();
		if (i_ < s_.size() && s_[i_] == ']') {
			++i_;
			return true;
		}
		while (true) {
			Value child;
			if (!ParseValue(child))
				return false;
			out.items.push_back(std::move(child));
			SkipWs();
			if (i_ >= s_.size())
				return Fail("unterminated array");
			if (s_[i_] == ',') {
				++i_;
				continue;
			}
			if (s_[i_] == ']') {
				++i_;
				return true;
			}
			return Fail("expected ',' or ']'");
		}
	}

	// Reads a run of 4 hex digits into `cp`. Returns false on bad input.
	bool ReadHex4(uint32_t &cp)
	{
		if (i_ + 4 > s_.size())
			return false;
		cp = 0;
		for (int k = 0; k < 4; ++k) {
			const char h = s_[i_ + k];
			cp <<= 4;
			if (h >= '0' && h <= '9')
				cp |= static_cast<uint32_t>(h - '0');
			else if (h >= 'a' && h <= 'f')
				cp |= static_cast<uint32_t>(h - 'a' + 10);
			else if (h >= 'A' && h <= 'F')
				cp |= static_cast<uint32_t>(h - 'A' + 10);
			else
				return false;
		}
		i_ += 4;
		return true;
	}

	static void AppendUtf8(std::string &out, uint32_t cp)
	{
		if (cp <= 0x7F) {
			out += static_cast<char>(cp);
		} else if (cp <= 0x7FF) {
			out += static_cast<char>(0xC0 | (cp >> 6));
			out += static_cast<char>(0x80 | (cp & 0x3F));
		} else if (cp <= 0xFFFF) {
			out += static_cast<char>(0xE0 | (cp >> 12));
			out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
			out += static_cast<char>(0x80 | (cp & 0x3F));
		} else {
			out += static_cast<char>(0xF0 | (cp >> 18));
			out += static_cast<char>(0x80 | ((cp >> 12) & 0x3F));
			out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
			out += static_cast<char>(0x80 | (cp & 0x3F));
		}
	}

	bool ParseString(std::string &out)
	{
		if (i_ >= s_.size() || s_[i_] != '"')
			return Fail("expected string");
		++i_;
		out.clear();
		while (i_ < s_.size()) {
			const unsigned char c = static_cast<unsigned char>(s_[i_++]);
			if (c == '"')
				return true;
			if (c != '\\') {
				out += static_cast<char>(c);
				continue;
			}
			if (i_ >= s_.size())
				return Fail("unterminated escape");
			const char e = s_[i_++];
			switch (e) {
			case '"': out += '"'; break;
			case '\\': out += '\\'; break;
			case '/': out += '/'; break;
			case 'b': out += '\b'; break;
			case 'f': out += '\f'; break;
			case 'n': out += '\n'; break;
			case 'r': out += '\r'; break;
			case 't': out += '\t'; break;
			case 'u': {
				uint32_t cp = 0;
				if (!ReadHex4(cp))
					return Fail("bad \\u escape");
				if (cp >= 0xD800 && cp <= 0xDBFF && i_ + 1 < s_.size() &&
				    s_[i_] == '\\' && s_[i_ + 1] == 'u') {
					const size_t save = i_;
					i_ += 2;
					uint32_t lo = 0;
					if (ReadHex4(lo) && lo >= 0xDC00 && lo <= 0xDFFF)
						cp = 0x10000 + ((cp - 0xD800) << 10) + (lo - 0xDC00);
					else
						i_ = save; // lone high surrogate: keep as-is
				}
				AppendUtf8(out, cp);
				break;
			}
			default:
				return Fail("unknown escape");
			}
		}
		return Fail("unterminated string");
	}

	bool ParseNumber(Value &out)
	{
		const size_t start = i_;
		if (i_ < s_.size() && (s_[i_] == '-' || s_[i_] == '+'))
			++i_;
		while (i_ < s_.size()) {
			const char c = s_[i_];
			if ((c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' ||
			    c == '+' || c == '-')
				++i_;
			else
				break;
		}
		if (i_ == start)
			return Fail("invalid number");
		const std::string tok = s_.substr(start, i_ - start);
		char *end = nullptr;
		const double v = std::strtod(tok.c_str(), &end);
		if (end == tok.c_str() || *end != '\0')
			return Fail("invalid number");
		out.type = Value::Type::Number;
		out.number = v;
		return true;
	}
};

void SerializeInto(const Value &v, std::string &out)
{
	switch (v.type) {
	case Value::Type::Null:
		out += "null";
		break;
	case Value::Type::Bool:
		out += v.boolean ? "true" : "false";
		break;
	case Value::Type::Number: {
		if (std::isfinite(v.number) && v.number == static_cast<double>(static_cast<int64_t>(v.number))) {
			out += std::to_string(static_cast<int64_t>(v.number));
		} else {
			std::ostringstream ss;
			ss << v.number;
			out += ss.str();
		}
		break;
	}
	case Value::Type::String:
		out += '"';
		out += Escape(v.text);
		out += '"';
		break;
	case Value::Type::Array: {
		out += '[';
		for (size_t k = 0; k < v.items.size(); ++k) {
			if (k)
				out += ',';
			SerializeInto(v.items[k], out);
		}
		out += ']';
		break;
	}
	case Value::Type::Object: {
		out += '{';
		for (size_t k = 0; k < v.members.size(); ++k) {
			if (k)
				out += ',';
			out += '"';
			out += Escape(v.members[k].first);
			out += "\":";
			SerializeInto(v.members[k].second, out);
		}
		out += '}';
		break;
	}
	}
}

} // namespace

bool Value::Parse(const std::string &input, Value &out, std::string *error)
{
	Parser p(input);
	if (!p.Run(out)) {
		if (error)
			*error = p.error();
		return false;
	}
	return true;
}

std::string Serialize(const Value &value)
{
	std::string out;
	SerializeInto(value, out);
	return out;
}

} // namespace geseki::json
