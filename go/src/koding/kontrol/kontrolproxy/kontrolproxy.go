package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"github.com/gorilla/sessions"
	"github.com/nranchev/go-libGeoIP"
	"github.com/rcrowley/goagain"
	"html/template"
	"io"
	"koding/kontrol/kontrolhelper"
	"koding/kontrol/kontrolproxy/proxyconfig"
	"koding/tools/config"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	log.SetPrefix(fmt.Sprintf("proxy [%5d] ", os.Getpid()))
}

type RabbitChannel struct {
	ReplyTo string
	Receive chan []byte
}

var templates = template.Must(template.ParseFiles(
	"go/templates/proxy/securepage.html",
	"go/templates/proxy/notfound.html",
	"go/templates/proxy/notactiveVM.html",
	"client/maintenance.html",
))

var proxyDB *proxyconfig.ProxyConfiguration
var amqpStream *AmqpStream
var connections = make(map[string]RabbitChannel)
var geoIP *libgeo.GeoIP
var hostname = kontrolhelper.CustomHostname()
var store = sessions.NewCookieStore([]byte("kontrolproxy-secret-key"))
var users = make(map[string]time.Time)
var usersLock sync.RWMutex

func main() {
	log.Printf("kontrol proxy started ")
	// open and read from DB
	var err error
	proxyDB, err = proxyconfig.Connect()
	if err != nil {
		log.Fatalf("proxyconfig mongodb connect: %s", err)
	}

	err = proxyDB.AddProxy(hostname)
	if err != nil {
		log.Println(err)
	}

	// load GeoIP db into memory
	dbFile := "GeoIP.dat"
	geoIP, err = libgeo.Load(dbFile)
	if err != nil {
		log.Printf("load GeoIP.dat: %s\n", err.Error())
	}

	// create amqpStream for rabbitmq proxyieng
	// amqpStream = setupAmqp()

	reverseProxy := &ReverseProxy{}

	// HTTPS handling
	portssl := strconv.Itoa(config.Current.Kontrold.Proxy.PortSSL)
	sslips := strings.Split(config.Current.Kontrold.Proxy.SSLIPS, ",")

	var (
		listener  net.Listener
		ppid      int
		listeners = make(map[string]net.Listener, 0)
	)

	for _, sslip := range sslips {
		cert, err := tls.LoadX509KeyPair(sslip+"_cert.pem", sslip+"_key.pem")
		if nil != err {
			log.Printf("https mode is disabled. please add cert.pem and key.pem files. %s %s", err, sslip)
			continue
		}

		addr := sslip + ":" + portssl
		listener, _, err = goagain.GetEnvs(addr)
		if err != nil {
			laddr, err := net.ResolveTCPAddr("tcp", addr)
			if nil != err {
				log.Fatalln(err)
			}

			listener, err = net.ListenTCP("tcp", laddr)
			if nil != err {
				log.Fatalln(err)
			}
		}

		listeners[addr] = listener
		listener = tls.NewListener(listener, &tls.Config{
			NextProtos:   []string{"http/1.1"},
			Certificates: []tls.Certificate{cert},
		})

		log.Printf("https mode is enabled. serving at :%s ...", portssl)
		go http.Serve(listener, reverseProxy)

	}

	// HTTP handling
	port := strconv.Itoa(config.Current.Kontrold.Proxy.Port)
	addr := "127.0.0.1:" + port
	listener, ppid, err = goagain.GetEnvs(addr)
	log.Printf("normal mode is enabled. serving at :%s ...", port)
	if err != nil {
		laddr, err := net.ResolveTCPAddr("tcp", ":"+port) // don't change this!
		if nil != err {
			log.Fatalln(err)
		}

		listener, err = net.ListenTCP("tcp", laddr)
		if nil != err {
			log.Fatalln(err)
		}
		go func() {
			err := http.Serve(listener, reverseProxy) // resume listening
			if err != nil {
				log.Println("normal mode is disabled", err)
			}
		}()
	} else {
		go func() {
			err := http.Serve(listener, reverseProxy) // resume listening
			if err != nil {
				log.Println("normal mode is disabled", err)
			}
		}()

		// Kill the parent, now that the child has started successfully.
		if err := goagain.KillParent(ppid); nil != err {
			log.Fatalln(err)
		}

	}
	listeners[addr] = listener

	// needed until all go routines are ready. otherwise the log below is printed earlier
	time.Sleep(time.Second * 2)
	log.Printf("started and waiting for signals on pid %d.\n\t* for a stop send SIGTERM.\n\t* for a new binary restart send SIGUSR2\n", os.Getpid())

	// Block the main goroutine awaiting signals.
	if err := goagain.AwaitSignals(listeners); nil != err {
		log.Fatalln(err)
	}

	// we need to close all listeners..
	for _, l := range listeners {
		if err := l.Close(); nil != err {
			log.Fatalln(err)
		}
		log.Printf("stopping listener: %s\n", l.Addr().String())
	}
	log.Printf("stopped current instance with pid %d\n", os.Getpid())
}

/*************************************************
*
*  modified version of go's reverseProxy source code
*  has support for dynamic target url, websockets and amqp
*
*  - arslan
*************************************************/

// ReverseProxy is an HTTP Handler that takes an incoming request and
// sends it to another server, proxying the response back to the
// client.
type ReverseProxy struct {
	// The transport used to perform proxy requests.
	// If nil, http.DefaultTransport is used.
	Transport http.RoundTripper
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func (p *ReverseProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// redirect http to https
	if req.TLS == nil && (req.Host == "koding.com" || req.Host == "www.koding.com") {
		http.Redirect(rw, req, "https://koding.com"+req.RequestURI, http.StatusMovedPermanently)
		return
	}

	// Display error when someone hits the main page
	if hostname == req.Host {
		io.WriteString(rw, "Hello kontrol proxy :)")
		return
	}

	outreq := new(http.Request)
	*outreq = *req // includes shallow copies of maps, but okay

	user, buf, err := populateUser(outreq)
	if err != nil {
		rw.WriteHeader(http.StatusNotFound)
		log.Printf("parsing incoming request from %s to %s: %s", outreq.RemoteAddr, outreq.Host, err.Error())
		if buf != nil { // if any pre rendered html is available, use that for error displaying
			p.copyResponse(rw, buf)
		} else {
			buf, err := executeTemplate("notfound.html", outreq.Host)
			if err != nil {
				log.Println("error executing template", err.Error())
				return
			}
			p.copyResponse(rw, buf)
		}
		return
	}

	target := user.Target

	fmt.Printf("proxy via db\t: %s --> %s\n", user.Domain.Domain, target.Host)

	switch user.Domain.Proxy.Mode {
	case "maintenance":
		p.copyResponse(rw, buf)
		return
	case "redirect":
		http.Redirect(rw, req, user.Target.String(), http.StatusTemporaryRedirect)
		return
	}

	switch user.Domain.LoadBalancer.Mode {
	case "sticky":
		sessionName := fmt.Sprintf("kodingproxy-%s-%s", outreq.Host, user.IP)
		session, _ := store.Get(req, sessionName)
		targetURL, ok := session.Values["GOSESSIONID"]
		if ok {
			target, err = url.Parse(targetURL.(string))
			if err != nil {
				io.WriteString(rw, fmt.Sprintf("{\"err\":\"%s\"}\n", err.Error()))
				return
			}
		} else {
			session.Values["GOSESSIONID"] = target.String()
			session.Save(outreq, rw)
		}
	}

	_, err = validate(user)
	if err != nil {
		if err == ErrSecurePage {
			sessionName := fmt.Sprintf("kodingproxy-%s-%s", outreq.Host, user.IP)
			// We're ignoring the error resulted from decoding an existing
			// session: Get() always returns a session, even if empty.
			session, _ := store.Get(req, sessionName)

			// Timeout for secure page. After timeout secure page is showed
			// again to the user
			session.Options = &sessions.Options{MaxAge: 20} //seconds

			_, ok := session.Values["securePage"]
			if !ok {
				session.Values["securePage"] = time.Now().String()
				session.Save(req, rw)
				err := templates.ExecuteTemplate(rw, "securepage.html", user)
				if err != nil {
					http.Error(rw, err.Error(), http.StatusInternalServerError)
				}
				return
			}
		} else {
			log.Printf("error validating user: %s", err.Error())
			io.WriteString(rw, fmt.Sprintf("{\"err\":\"%s\"}\n", err.Error()))
			return
		}
	}

	// Smart handling incoming request path/query, example:
	// incoming : foo.com/dir
	// target	: bar.com/base
	// proxy to : bar.com/base/dir
	outreq.URL.Scheme = target.Scheme
	outreq.URL.Host = target.Host
	outreq.URL.Path = singleJoiningSlash(target.Path, outreq.URL.Path)

	// incoming : foo.com/name=arslan
	// target	: bar.com/q=example
	// proxy to : bar.com/q=example&name=arslan
	if target.RawQuery == "" || outreq.URL.RawQuery == "" {
		outreq.URL.RawQuery = target.RawQuery + outreq.URL.RawQuery
	} else {
		outreq.URL.RawQuery = target.RawQuery + "&" + outreq.URL.RawQuery
	}

	outreq.Proto = "HTTP/1.1"
	outreq.ProtoMajor = 1
	outreq.ProtoMinor = 1
	outreq.Close = false

	go func() {
		if !isUserRegistered(user.IP) {
			go registerUser(user.IP)
			go logDomainRequests(outreq.Host)
			go logProxyStat(hostname, user.Country)
		}
	}()

	// if connection is of type websocket, hijacking is used instead of http proxy
	// https://groups.google.com/d/msg/golang-nuts/KBx9pDlvFOc/edt4iad96nwJ
	if isWebsocket(outreq) {
		rConn, err := net.Dial("tcp", outreq.URL.Host)
		if err != nil {
			http.Error(rw, "Error contacting backend server.", http.StatusInternalServerError)
			log.Printf("Error dialing websocket backend %s: %v", outreq.URL.Host, err)
			return
		}

		hj, ok := rw.(http.Hijacker)
		if !ok {
			http.Error(rw, "Not a hijacker?", http.StatusInternalServerError)
			return
		}

		conn, _, err := hj.Hijack()
		if err != nil {
			log.Printf("Hijack error: %v", err)
			return
		}
		defer conn.Close()
		defer rConn.Close()

		err = req.Write(rConn)
		if err != nil {
			log.Printf("Error copying request to target: %v", err)
			return
		}

		errc := make(chan error, 2)
		cp := func(dst io.Writer, src io.Reader) {
			_, err := io.Copy(dst, src)
			errc <- err
		}
		go cp(rConn, conn)
		go cp(conn, rConn)
		<-errc
		return
	} else {
		transport := p.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}

		// Remove hop-by-hop headers to the backend.  Especially
		// important is "Connection" because we want a persistent
		// connection, regardless of what the client sent to us.  This
		// is modifying the same underlying map from req (shallow
		// copied above) so we only copy it if necessary.
		copiedHeaders := false
		for _, h := range hopHeaders {
			if outreq.Header.Get(h) != "" {
				if !copiedHeaders {
					outreq.Header = make(http.Header)
					copyHeader(outreq.Header, req.Header)
					copiedHeaders = true
				}
				outreq.Header.Del(h)
			}
		}

		if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			// If we aren't the first proxy retain prior
			// X-Forwarded-For information as a comma+space
			// separated list and fold multiple headers into one.
			if prior, ok := outreq.Header["X-Forwarded-For"]; ok {
				clientIP = strings.Join(prior, ", ") + ", " + clientIP
			}
			outreq.Header.Set("X-Forwarded-For", clientIP)
		}

		res := new(http.Response)

		if !hasPort(outreq.URL.Host) {
			outreq.URL.Host = addPort(outreq.URL.Host, "80")
		}

		res, err = transport.RoundTrip(outreq)
		if err != nil {
			io.WriteString(rw, fmt.Sprint(err))
			return
		}
		defer res.Body.Close()

		// rabbitKey, err := lookupRabbitKey(user.Username, user.Servicename, user.Key)
		// if err != nil {
		// 	// add :80 if not available
		// } else {
		// 	fmt.Println("connection via rabbitmq")
		// 	res, err = rabbitTransport(outreq, user, rabbitKey)
		// 	if err != nil {
		// 		log.Printf("rabbit proxy %s", err.Error())
		// 		io.WriteString(rw, fmt.Sprintf("{\"err\":\"%s\"}\n", err.Error()))
		// 		return
		// 	}
		// }

		copyHeader(rw.Header(), res.Header)
		rw.WriteHeader(res.StatusCode)
		p.copyResponse(rw, res.Body)
		return
	}

}

func (p *ReverseProxy) copyResponse(dst io.Writer, src io.Reader) {
	io.Copy(dst, src)
}

/*************************************************
*
*  unique IP handling and cleaner
*
*  - arslan
*************************************************/
func registerUser(ip string) {
	usersLock.Lock()
	defer usersLock.Unlock()
	users[ip] = time.Now()
	if len(users) == 1 {
		go cleaner()
	}
}

// The goroutine basically does this: as long as there are users in the map, it
// finds the one it should be deleted next, sleeps until it's time to delete it
// (one hour - time since user registration) and deletes it.  If there are no
// users, the goroutine exits and a new one is created the next time a user is
// registered. The time.Sleep goes toward zero, thus it will not lock the
// for iterator forever.
func cleaner() {
	usersLock.RLock()
	for len(users) > 0 {
		var nextTime time.Time
		var nextUser string
		for u, t := range users {
			if nextTime.IsZero() || t.Before(nextTime) {
				nextTime = t
				nextUser = u
			}
		}
		usersLock.RUnlock()
		// negative duration is no-op, means it will not panic
		time.Sleep(time.Hour - time.Now().Sub(nextTime))
		usersLock.Lock()
		log.Println("deleting user from internal map", nextUser)
		delete(users, nextUser)
		usersLock.Unlock()
		usersLock.RLock()
	}
	usersLock.RUnlock()
}

// Needed to avoid race condition between multiple go routines
func isUserRegistered(ip string) bool {
	usersLock.RLock()
	defer usersLock.RUnlock()
	_, ok := users[ip]
	return ok
}

/*************************************************
*
*  util functions
*
*  - arslan
*************************************************/
// Given a string of the form "host", "host:port", or "[ipv6::address]:port",
// return true if the string includes a port.
func hasPort(s string) bool { return strings.LastIndex(s, ":") > strings.LastIndex(s, "]") }

// Given a string of the form "host", "port", returns "host:port"
func addPort(host, port string) string {
	if ok := hasPort(host); ok {
		return host
	}

	return host + ":" + port
}

// Check if a server is alive or not
func checkServer(host string) error {
	c, err := net.DialTimeout("tcp", host, time.Second*5)
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

func executeTemplate(filename string, data interface{}) (io.Reader, error) {
	buf := new(bytes.Buffer)
	err := templates.ExecuteTemplate(buf, filename, data)
	if err != nil {
		return buf, err
	}

	return buf, nil
}
