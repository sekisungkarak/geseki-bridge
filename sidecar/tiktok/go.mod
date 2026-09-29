module github.com/sekisungkarak/geseki-bridge/sidecar/tiktok

go 1.23.0

require github.com/steampoweredtaco/gotiktoklive v0.0.4

require (
	github.com/benbjohnson/clock v1.3.0 // indirect
	github.com/erni27/imcache v1.2.1 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.1.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	go.uber.org/ratelimit v0.3.1 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.20.0 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
)

// Vendored so we can patch upstream bugs that are still unfixed there:
//   * toUser() tested AvatarLarge but read AvatarJpg, so every user arrived
//     with an empty ProfilePicture (upstream issue #18, "No Picture returned
//     for User and Gifts"). The vendored copy picks the largest image that
//     actually exists and exposes badge image URLs.
replace github.com/steampoweredtaco/gotiktoklive => ./third_party/gotiktoklive
