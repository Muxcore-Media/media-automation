module github.com/Muxcore-Media/media-automation

go 1.26.4

require (
	github.com/Muxcore-Media/contracts-downloader v0.0.0
	github.com/Muxcore-Media/contracts-indexer v0.0.0
	github.com/Muxcore-Media/core v0.4.0
	github.com/Muxcore-Media/core/pkg/contracts v0.0.0
	github.com/Muxcore-Media/core/sdk/go/client v0.1.0
	github.com/Muxcore-Media/core/sdk/go/module v0.1.0
	github.com/Muxcore-Media/media-custom-formats v0.1.0
	github.com/Muxcore-Media/media-movies v0.0.0
	github.com/Muxcore-Media/media-scanner v0.0.0
	github.com/Muxcore-Media/media-tvshows v0.1.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.11
	modernc.org/sqlite v1.54.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260610212136-7ab31c22f7ad // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/Muxcore-Media/core => ../core

replace github.com/Muxcore-Media/core/sdk/go/module => ../core/sdk/go/module

replace github.com/Muxcore-Media/core/sdk/go/client => ../core/sdk/go/client

replace github.com/Muxcore-Media/core/pkg/contracts => ../core/pkg/contracts

replace github.com/Muxcore-Media/downloader-native-torrent => ../downloader-native-torrent

replace github.com/Muxcore-Media/contracts-downloader => ../contracts-downloader

replace github.com/Muxcore-Media/contracts-indexer => ../contracts-indexer

replace github.com/Muxcore-Media/media-custom-formats => ../media-custom-formats

replace github.com/Muxcore-Media/media-movies => ../media-movies

replace github.com/Muxcore-Media/media-scanner => ../media-scanner

replace github.com/Muxcore-Media/media-tvshows => ../media-tvshows
