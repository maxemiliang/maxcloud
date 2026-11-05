package auth

import "net"

// listen is net.Listen, replaceable in tests.
var listen = func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) }
