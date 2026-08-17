module github.com/denizsincar29/yastation

go 1.23

toolchain go1.24.4

require github.com/coder/websocket v1.8.15

require (
	github.com/ergochat/readline v0.1.3
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
)

require (
	golang.org/x/sys v0.15.0 // indirect
	golang.org/x/text v0.9.0 // indirect
)

replace golang.org/x/sys => github.com/golang/sys v0.28.0

replace golang.org/x/text => github.com/golang/text v0.9.0
