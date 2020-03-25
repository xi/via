// Simple pubsub server inspired by https://patchbay.pub
//
// Usage: via [-v] [[host]:port]
// curl http://localhost:8001/someid  # block
// curl http://localhost:8001/someid?sse  # server sent event stream
// curl http://localhost:8001/someid -d somedata
//
// curl http://localhost:8001/someid:somepassword?sse
// curl http://localhost:8001/someid  # 403
// curl http://localhost:8001/someid -d somedata  # 200
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Topic struct {
	sync.RWMutex
	channels map[chan []byte]bool
	password string
}

var mux = &sync.RWMutex{}
var topics = make(map[string]Topic)
var verbose = false

func splitPassword(combined string) (string, string) {
	split := strings.SplitN(combined, ":", 2)
	if len(split) == 2 {
		return split[0], split[1]
	} else {
		return combined, ""
	}
}

func pushChannel(key string, password string, ch chan []byte) bool {
	mux.RLock()
	topic, ok := topics[key]
	mux.RUnlock()

	if !ok {
		topic = Topic{
			channels: make(map[chan []byte]bool, 0),
			password: password,
		}
		mux.Lock()
		topics[key] = topic
		mux.Unlock()
	} else if topic.password != password {
		return false
	}

	topic.Lock()
	topic.channels[ch] = true
	topic.Unlock()

	return true
}

func popChannel(key string, ch chan []byte) {
	mux.RLock()
	topic := topics[key]
	mux.RUnlock()

	topic.Lock()
	delete(topic.channels, ch)
	topic.Unlock()

	if len(topic.channels) == 0 {
		if verbose {
			log.Println("clearing topic", key)
		}
		mux.Lock()
		delete(topics, key)
		mux.Unlock()
	}
}

func post(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	if password != "" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request body:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	mux.RLock()
	topic, ok := topics[key]
	mux.RUnlock()

	if !ok {
		return
	}

	topic.RLock()
	defer topic.RUnlock()

	for channel := range topic.channels {
		go func(ch chan []byte) {
			ch <- body
		}(channel)
	}
}

func getBlocking(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	ch := make(chan []byte)
	allowed := pushChannel(key, password, ch)
	if !allowed {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defer popChannel(key, ch)

	ctx := r.Context()

	select {
	case <-ctx.Done():
		return
	case s := <-ch:
		w.Write(s)
	}
}

func getSse(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	ch := make(chan []byte)
	allowed := pushChannel(key, password, ch)
	if !allowed {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defer popChannel(key, ch)

	ctx := r.Context()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case s := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", s)
			flusher.Flush()
		}
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	if verbose {
		log.Println(r.Method, r.URL)
	}

	if r.Method == "GET" {
		if r.URL.RawQuery == "sse" {
			getSse(w, r)
		} else {
			getBlocking(w, r)
		}
	} else if r.Method == "POST" {
		post(w, r)
	} else {
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "via [-v] [[host]:port]\n")
		flag.PrintDefaults()
	}

	flag.BoolVar(&verbose, "v", false, "enable verbose logs")
	flag.Parse()

	addr := "localhost:8001"
	if len(flag.Args()) > 0 {
		addr = flag.Args()[0]
	}
	if addr[0] == ':' {
		addr = "localhost" + addr
	}

	http.HandleFunc("/", handler)
	log.Printf("Serving on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
