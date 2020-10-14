package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
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
var dir = ""

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

func getPath(key string) string {
	hash := base64.URLEncoding.EncodeToString([]byte(key))
	return path.Join(dir, hash)
}

func postMsg(w http.ResponseWriter, r *http.Request) {
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

func getMsg(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	ch := make(chan []byte)
	allowed := pushChannel(key, password, ch)
	if !allowed {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	defer popChannel(key, ch)

	ctx := r.Context()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, ": ping\n\n")
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

func putStore(w http.ResponseWriter, r *http.Request) {
	path := getPath(r.URL.Path)

	content, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request body:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	err = ioutil.WriteFile(path, content, 0644)
	if err != nil {
		log.Println("error writing to file:", path, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func getStore(w http.ResponseWriter, r *http.Request) {
	path := getPath(r.URL.Path)

	content, err := ioutil.ReadFile(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err != nil {
		log.Println("error reading from file:", path, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Write(content)
}

func deleteStore(w http.ResponseWriter, r *http.Request) {
	path := getPath(r.URL.Path)

	err := os.Remove(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err != nil {
		log.Println("error removing file:", path, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func handleMsg(w http.ResponseWriter, r *http.Request) {
	if verbose {
		log.Println(r.Method, r.URL)
	}

	if r.Method == http.MethodGet {
		getMsg(w, r)
	} else if r.Method == http.MethodPost {
		postMsg(w, r)
	} else {
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func handleStore(w http.ResponseWriter, r *http.Request) {
	if verbose {
		log.Println(r.Method, r.URL)
	}

	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		putStore(w, r)
	} else if r.Method == http.MethodGet || r.Method == http.MethodHead {
		getStore(w, r)
	} else if r.Method == http.MethodDelete {
		deleteStore(w, r)
	} else {
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "via [-v] [port]\n")
		flag.PrintDefaults()
	}

	flag.BoolVar(&verbose, "v", false, "enable verbose logs")
	flag.StringVar(&dir, "d", ".", "directory for storage")
	flag.Parse()

	addr := "localhost:8001"
	if len(flag.Args()) > 0 {
		addr = fmt.Sprintf("localhost:%s", flag.Args()[0])
	}

	http.HandleFunc("/msg/", handleMsg)
	http.HandleFunc("/store/", handleStore)

	log.Printf("Serving on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
