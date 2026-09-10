package config

import (
	"net"
	"testing"

	"github.com/alecthomas/kong"
)

func TestCLIListenAddress(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"local by default", nil, "127.0.0.1:9024"},
		{"explicit remote access", []string{"--host=0.0.0.0", "--port=8080"}, "0.0.0.0:8080"},
		{"IPv6 loopback", []string{"--host=::1"}, "[::1]:9024"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cli Cli
			parser, err := kong.New(&cli)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			if got := cli.ListenAddress(); got != test.want {
				t.Fatalf("listen address = %q, want %q", got, test.want)
			}
			if test.args == nil {
				cli.Port = 0
				listener, err := net.Listen("tcp", cli.ListenAddress())
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				if !listener.Addr().(*net.TCPAddr).IP.IsLoopback() {
					t.Fatalf("default listener is not loopback: %s", listener.Addr())
				}
			}
		})
	}
}

func TestCLIRejectsInvalidHost(t *testing.T) {
	for _, host := range []string{"", " ", "invalid", "127.0.0.1:9024"} {
		t.Run(host, func(t *testing.T) {
			var cli Cli
			parser, err := kong.New(&cli)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.Parse([]string{"--host=" + host}); err == nil {
				t.Fatal("expected invalid host to be rejected")
			}
		})
	}
}
