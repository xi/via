package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
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
	hasHistory bool
	history []Msg
	lastId int
}

var mux = &sync.RWMutex{}
var topics = make(map[string]*Topic)
var verbose = false
var maxHistorySize = 100
var dir = ""

func splitPassword(combined string) (string, string) {
	split := strings.SplitN(combined, ":", 2)
	if len(split) == 2 {
		return split[0], split[1]
	} else {
		return combined, ""
	}
}

func getStorePath(key string) string {
	hash := base64.URLEncoding.EncodeToString([]byte(key))
	return path.Join(dir, hash)
}

func (topic Topic) storeHistory(key string) {
	topic.Lock()
	defer topic.Unlock()

	path := getStorePath(fmt.Sprintf("%s:%s", key, topic.password))

	content, err := json.Marshal(topic.history)
	if err != nil {
		log.Println("error storing history:", err)
		return
	}

	err = ioutil.WriteFile(path, content, 0644)
	if err != nil {
		log.Println("error storing history:", err)
		return
	}
}

func (topic *Topic) restoreHistory(key string) {
	topic.Lock()
	defer topic.Unlock()

	path := getStorePath(fmt.Sprintf("%s:%s", key, topic.password))

	content, err := ioutil.ReadFile(path)
	if err != nil {
		log.Println("error restoring history:", err)
		return
	}

	var history []Msg
	err = json.Unmarshal(content, &history)
	if err != nil {
		log.Println("error restoring history:", err)
		return
	}

	topic.history = history
	if len(history) > 0 {
		topic.lastId = history[len(history)-1].Id
	}
}

func (topic *Topic) post(data []byte) {
	topic.Lock()
	defer topic.Unlock()

	topic.lastId += 1
	msg := Msg{topic.lastId, data}

	if topic.hasHistory {
		topic.history = append(topic.history, msg)

		for len(topic.history) > maxHistorySize {
			topic.history = topic.history[1:]
		}
	}

	for channel := range topic.channels {
		go func(ch chan Msg) {
			ch <- msg
		}(channel)
	}
}

func pushChannel(key string, password string, ch chan Msg, lastId int) bool {
	mux.RLock()
	topic, ok := topics[key]
	mux.RUnlock()

	if !ok {
		topic = &Topic{
			channels: make(map[chan Msg]bool, 0),
			password: password,
			hasHistory: strings.HasPrefix(key, "/hmsg/"),
			history: make([]Msg, 0),
			lastId: 0,
		}
		if topic.hasHistory {
			topic.restoreHistory(key)
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

	if topic.hasHistory {
		go func(t Topic) {
			for _, msg := range t.history {
				if msg.Id > lastId {
					ch <- msg
				}
			}
		}(*topic)
	}

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

	topic.post(body)

	if topic.hasHistory {
		topic.storeHistory(key)
	}
}

func get(w http.ResponseWriter, r *http.Request) {
	key, password := splitPassword(r.URL.Path)

	lastId, err := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	if err != nil {
		lastId = 0
	}

	ch := make(chan Msg)
	allowed := pushChannel(key, password, ch, lastId)
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

func handler(w http.ResponseWriter, r *http.Request) {
	if verbose {
		log.Println(r.Method, r.URL)
	}

	if r.Method == http.MethodGet {
		get(w, r)
	} else if r.Method == http.MethodPost {
		post(w, r)
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

	http.HandleFunc("/msg/", handler)
	http.HandleFunc("/hmsg/", handler)

	log.Printf("Serving on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
