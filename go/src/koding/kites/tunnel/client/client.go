package main

import (
	"bufio"
	"fmt"
	"koding/kites/tunnel/protocol"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
)

var serverAddr = "127.0.0.1:7000"
var localAddr = "127.0.0.1:5000"
var registerPath = "_kdtunnel_"

func init() {
	log.SetOutput(os.Stdout)
	log.SetPrefix("tunnel-client ")
	log.SetFlags(log.Lmicroseconds)
}

func main() {
	remoteConn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		log.Println("dial remote err: %s", err)
		return
	}
	defer remoteConn.Close()

	err = register(remoteConn)
	if err != nil {
		log.Println(err)
		return
	}

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("dial local err: %s\n", err)
		return
	}
	defer localConn.Close()

	server := httputil.NewServerConn(remoteConn, nil)
	clientConn := httputil.NewClientConn(localConn, nil)

	for {
		req, err := server.Read()
		if err != nil {
			fmt.Println("Server read", err)
			return
		}

		fmt.Println(req.RemoteAddr, req.URL.String(), req.Host, req.RequestURI)

		resp, err := clientConn.Do(req)
		if err != nil {
			fmt.Println("coudlnt do request")
		}

		fmt.Println("resp status", resp.Status, resp.Header)

		server.Write(req, resp)

	}
}

func register(remoteConn net.Conn) error {
	remoteAddr := fmt.Sprintf("http://%s%s", serverAddr, protocol.RegisterPath)
	req, err := http.NewRequest("CONNECT", remoteAddr, nil)
	if err != nil {
		return fmt.Errorf("CONNECT", err)
	}

	req.Header.Set("Username", "fatih")
	req.Write(remoteConn)

	resp, err := http.ReadResponse(bufio.NewReader(remoteConn), req)
	if err != nil {
		return fmt.Errorf("read response", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.Status != protocol.Connected {
		return fmt.Errorf("Non-200 response from proxy server: %s", resp.Status)
	}

	fmt.Println(resp.Status)
	return nil
}
