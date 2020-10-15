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

type Msg struct {
	Id int
	Data []byte
}

type Topic struct {
	sync.RWMutex
	channels map[chan Msg]bool
	password string
	lastId int
}

var mux = &sync.RWMutex{}
var topics = make(map[string]*Topic)
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

func pushChannel(key string, password string, ch chan Msg) bool {
	mux.RLock()
	topic, ok := topics[key]
	mux.RUnlock()

	if !ok {
		topic = &Topic{
			channels: make(map[chan Msg]bool, 0),
			password: password,
			lastId: 0,
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

func popChannel(key string, ch chan Msg) {
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

func getStorePath(key string) string {
	rel := strings.TrimPrefix(key, "/store/")
	hash := base64.URLEncoding.EncodeToString([]byte(rel))
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

	topic.lastId += 1
	msg := Msg{topic.lastId, body}

	for channel := range topic.channels {
		go func(ch chan Msg) {
			ch <- msg
		}(channel)
	}
}

func getMsg(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	ch := make(chan Msg)
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
		case msg := <-ch:
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", msg.Id, msg.Data)
			flusher.Flush()
		}
	}
}

func putStore(w http.ResponseWriter, r *http.Request) {
	path := getStorePath(r.URL.Path)

	content, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Println("error reading request body:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	err = ioutil.WriteFile(path, content, 0644)
	if err != nil {
		log.Println("error writing to file:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func getStore(w http.ResponseWriter, r *http.Request) {
	path := getStorePath(r.URL.Path)

	content, err := ioutil.ReadFile(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err != nil {
		log.Println("error reading from file:", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Write(content)
}

func deleteStore(w http.ResponseWriter, r *http.Request) {
	path := getStorePath(r.URL.Path)

	err := os.Remove(path)
	if os.IsNotExist(err) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	} else if err != nil {
		log.Println("error removing file:", err)
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
		fmt.Fprintf(os.Stderr, "via [-v] [-d storage_dir] [port]\n")
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
